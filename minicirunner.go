package minicirunner

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Limits enforced during parsing.
const (
	MaxInputBytes         = 1 << 20 // 1 MiB
	MaxYAMLDepth          = 32
	MaxMatrixCombinations = 256
	MaxExpandedJobs       = 100
	DefaultTimeout        = 30 * time.Minute
	MinTimeout            = time.Millisecond
	MaxTimeout            = 24 * time.Hour
)

// Pipeline represents a parsed and validated CI pipeline with expanded matrix legs.
type Pipeline struct {
	Version int
	Jobs    []*Job
}

// Job represents a single expanded job leg in a pipeline.
type Job struct {
	ID        string
	BaseID    string
	Image     string
	Needs     []string
	Matrix    map[string]string
	Env       map[string]string
	Timeout   time.Duration
	Cache     *CacheSpec
	Steps     []Step
	Artifacts []string
}

// CacheSpec defines the cache key expression and relative workspace paths to persist.
type CacheSpec struct {
	Key   string   `yaml:"key"`
	Paths []string `yaml:"paths"`
}

// Step is a single command execution within a job.
type Step struct {
	Name string `yaml:"name"`
	Run  string `yaml:"run"`
}

type rawPipeline struct {
	Version int                `yaml:"version"`
	Jobs    map[string]*rawJob `yaml:"jobs"`
}

type rawJob struct {
	Image     string              `yaml:"image"`
	Needs     []string            `yaml:"needs"`
	Matrix    map[string][]string `yaml:"matrix"`
	Env       map[string]string   `yaml:"env"`
	Timeout   string              `yaml:"timeout"`
	Cache     *CacheSpec          `yaml:"cache"`
	Steps     []Step              `yaml:"steps"`
	Artifacts []string            `yaml:"artifacts"`
}

// Parse decodes and validates a YAML pipeline definition.
func Parse(data []byte) (*Pipeline, error) {
	if len(data) > MaxInputBytes {
		return nil, newErrorf(ErrCodeInputTooLarge, "pipeline input of %d bytes exceeds limit of %d bytes", len(data), MaxInputBytes)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, newErrorf(ErrCodeParse, "invalid YAML: %v", err)
	}
	if root.Kind == 0 {
		return nil, newError(ErrCodeParse, "empty YAML document")
	}
	if err := checkNodeTree(&root, 1); err != nil {
		return nil, err
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var raw rawPipeline
	if err := dec.Decode(&raw); err != nil {
		return nil, newErrorf(ErrCodeParse, "failed to decode pipeline: %v", err)
	}
	if raw.Version != 1 {
		return nil, newErrorf(ErrCodeParse, "unsupported pipeline version %d (want 1)", raw.Version)
	}
	if len(raw.Jobs) == 0 {
		return nil, newError(ErrCodeParse, "pipeline must define at least one job")
	}

	baseIDs := make([]string, 0, len(raw.Jobs))
	for id := range raw.Jobs {
		baseIDs = append(baseIDs, id)
	}
	sort.Strings(baseIDs)

	// Validate base job existence for needs and check for cycles on base jobs.
	for _, id := range baseIDs {
		rj := raw.Jobs[id]
		if rj == nil {
			return nil, newErrorf(ErrCodeParse, "job %q is empty", id)
		}
		for _, need := range rj.Needs {
			if _, ok := raw.Jobs[need]; !ok {
				return nil, newErrorf(ErrCodeUnknownJob, "job %q needs unknown job %q", id, need)
			}
		}
	}
	if err := detectBaseCycles(baseIDs, raw.Jobs); err != nil {
		return nil, err
	}

	// Expand jobs and validate fields.
	expandedByBase := make(map[string][]*Job, len(baseIDs))
	var allJobs []*Job

	for _, baseID := range baseIDs {
		if strings.TrimSpace(baseID) == "" {
			return nil, newError(ErrCodeParse, "job id must not be blank")
		}
		rj := raw.Jobs[baseID]
		legs, err := validateAndExpandJob(baseID, rj)
		if err != nil {
			return nil, err
		}
		expandedByBase[baseID] = legs
		allJobs = append(allJobs, legs...)
		if len(allJobs) > MaxExpandedJobs {
			return nil, newErrorf(ErrCodeMatrixTooLarge, "total expanded jobs exceeds limit of %d", MaxExpandedJobs)
		}
	}

	// Wire expanded needs: a need on a base job waits for all its expanded legs.
	for _, baseID := range baseIDs {
		rj := raw.Jobs[baseID]
		var resolvedNeeds []string
		seenNeed := make(map[string]bool)
		for _, needBase := range rj.Needs {
			for _, targetLeg := range expandedByBase[needBase] {
				if !seenNeed[targetLeg.ID] {
					seenNeed[targetLeg.ID] = true
					resolvedNeeds = append(resolvedNeeds, targetLeg.ID)
				}
			}
		}
		sort.Strings(resolvedNeeds)
		for _, leg := range expandedByBase[baseID] {
			leg.Needs = resolvedNeeds
		}
	}

	sort.Slice(allJobs, func(i, j int) bool {
		return allJobs[i].ID < allJobs[j].ID
	})

	return &Pipeline{
		Version: raw.Version,
		Jobs:    allJobs,
	}, nil
}

func checkNodeTree(n *yaml.Node, depth int) error {
	if depth > MaxYAMLDepth {
		return newErrorf(ErrCodeTooDeep, "YAML nesting depth exceeds limit of %d", MaxYAMLDepth)
	}
	if n.Kind == yaml.AliasNode || n.Alias != nil {
		return newError(ErrCodeAliasesNotAllowed, "YAML aliases are not allowed")
	}
	for _, child := range n.Content {
		childDepth := depth
		if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
			childDepth = depth + 1
		}
		if err := checkNodeTree(child, childDepth); err != nil {
			return err
		}
	}
	return nil
}

func detectBaseCycles(baseIDs []string, jobs map[string]*rawJob) error {
	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)
	state := make(map[string]int, len(baseIDs))
	var visit func(id string) error
	visit = func(id string) error {
		state[id] = visiting
		for _, dep := range jobs[id].Needs {
			if state[dep] == visiting {
				return newErrorf(ErrCodeCycle, "dependency cycle detected involving job %q and %q", id, dep)
			}
			if state[dep] == unvisited {
				if err := visit(dep); err != nil {
					return err
				}
			}
		}
		state[id] = visited
		return nil
	}
	for _, id := range baseIDs {
		if state[id] == unvisited {
			if err := visit(id); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateAndExpandJob(baseID string, rj *rawJob) ([]*Job, error) {
	if strings.TrimSpace(rj.Image) == "" {
		return nil, newErrorf(ErrCodeParse, "job %q is missing required field image", baseID)
	}
	if len(rj.Steps) == 0 {
		return nil, newErrorf(ErrCodeParse, "job %q must contain at least one step", baseID)
	}
	for i, step := range rj.Steps {
		if strings.TrimSpace(step.Run) == "" {
			return nil, newErrorf(ErrCodeParse, "job %q step %d has an empty run command", baseID, i)
		}
	}

	timeout := DefaultTimeout
	if rj.Timeout != "" {
		d, err := time.ParseDuration(rj.Timeout)
		if err != nil || d < MinTimeout || d > MaxTimeout {
			return nil, newErrorf(ErrCodeInvalidTimeout, "job %q has invalid timeout %q (must be between 1ms and 24h)", baseID, rj.Timeout)
		}
		timeout = d
	}

	if rj.Cache != nil {
		if strings.TrimSpace(rj.Cache.Key) == "" {
			return nil, newErrorf(ErrCodeParse, "job %q cache key must not be empty", baseID)
		}
		if len(rj.Cache.Paths) == 0 {
			return nil, newErrorf(ErrCodeParse, "job %q cache paths must not be empty", baseID)
		}
		for _, p := range rj.Cache.Paths {
			if err := validateRelativePath(p); err != nil {
				return nil, err
			}
		}
	}

	for _, p := range rj.Artifacts {
		if err := validateRelativePath(p); err != nil {
			return nil, err
		}
	}

	matrixKeys := make([]string, 0, len(rj.Matrix))
	for k := range rj.Matrix {
		matrixKeys = append(matrixKeys, k)
	}
	sort.Strings(matrixKeys)

	combinations, err := expandMatrix(baseID, matrixKeys, rj.Matrix)
	if err != nil {
		return nil, err
	}

	// Validate expressions in cache.key, env values, and step.run for each combination.
	legs := make([]*Job, 0, len(combinations))
	for _, combo := range combinations {
		legID := baseID
		if len(matrixKeys) > 0 {
			parts := make([]string, len(matrixKeys))
			for i, k := range matrixKeys {
				parts[i] = fmt.Sprintf("%s=%s", k, combo[k])
			}
			legID = fmt.Sprintf("%s (%s)", baseID, strings.Join(parts, ", "))
		}

		if rj.Cache != nil {
			if err := validateExpressions(rj.Cache.Key, combo); err != nil {
				return nil, err
			}
		}
		envCopy := make(map[string]string, len(rj.Env))
		for k, v := range rj.Env {
			if err := validateExpressions(v, combo); err != nil {
				return nil, err
			}
			envCopy[k] = v
		}
		stepsCopy := make([]Step, len(rj.Steps))
		for i, s := range rj.Steps {
			if err := validateExpressions(s.Run, combo); err != nil {
				return nil, err
			}
			stepsCopy[i] = s
		}

		var cacheCopy *CacheSpec
		if rj.Cache != nil {
			pathsCopy := make([]string, len(rj.Cache.Paths))
			copy(pathsCopy, rj.Cache.Paths)
			cacheCopy = &CacheSpec{
				Key:   rj.Cache.Key,
				Paths: pathsCopy,
			}
		}
		artifactsCopy := make([]string, len(rj.Artifacts))
		copy(artifactsCopy, rj.Artifacts)

		legs = append(legs, &Job{
			ID:        legID,
			BaseID:    baseID,
			Image:     rj.Image,
			Matrix:    combo,
			Env:       envCopy,
			Timeout:   timeout,
			Cache:     cacheCopy,
			Steps:     stepsCopy,
			Artifacts: artifactsCopy,
		})
	}

	return legs, nil
}

func expandMatrix(baseID string, keys []string, matrix map[string][]string) ([]map[string]string, error) {
	if len(keys) == 0 {
		return []map[string]string{{}}, nil
	}
	total := 1
	for _, k := range keys {
		vals := matrix[k]
		if len(vals) == 0 {
			return nil, newErrorf(ErrCodeParse, "job %q matrix key %q must have at least one value", baseID, k)
		}
		total *= len(vals)
		if total > MaxMatrixCombinations {
			return nil, newErrorf(ErrCodeMatrixTooLarge, "job %q matrix has more than %d combinations", baseID, MaxMatrixCombinations)
		}
	}

	result := []map[string]string{{}}
	for _, k := range keys {
		vals := matrix[k]
		next := make([]map[string]string, 0, len(result)*len(vals))
		for _, existing := range result {
			for _, v := range vals {
				m := make(map[string]string, len(existing)+1)
				for ek, ev := range existing {
					m[ek] = ev
				}
				m[k] = v
				next = append(next, m)
			}
		}
		result = next
	}
	return result, nil
}

func validateRelativePath(p string) error {
	if p == "" {
		return newError(ErrCodeInvalidPath, "path must not be empty")
	}
	if strings.Contains(p, "\\") {
		return newErrorf(ErrCodeInvalidPath, "path %q must use forward slashes", p)
	}
	if strings.HasPrefix(p, "/") {
		return newErrorf(ErrCodeInvalidPath, "path %q must be relative", p)
	}
	cleaned := path.Clean(p)
	if cleaned != p || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return newErrorf(ErrCodeInvalidPath, "path %q must be clean, relative and without '..'", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "." || seg == "" {
			return newErrorf(ErrCodeInvalidPath, "path %q contains invalid segment", p)
		}
	}
	return nil
}

var (
	matrixExprRe    = regexp.MustCompile(`^matrix\.([A-Za-z0-9_-]+)$`)
	hashFilesExprRe = regexp.MustCompile(`^hashFiles\("([^"]+)"\)$`)
)

func validateExpressions(s string, matrix map[string]string) error {
	_, err := evalExpressions(s, matrix, nil)
	return err
}

func evalExpressions(s string, matrix map[string]string, hashFn func(glob string) (string, error)) (string, error) {
	var out strings.Builder
	rest := s
	for {
		start := strings.Index(rest, "${{")
		if start == -1 {
			if strings.Contains(rest, "}}") {
				return "", newErrorf(ErrCodeParse, "unmatched closing expression delimiter in %q", s)
			}
			out.WriteString(rest)
			break
		}
		out.WriteString(rest[:start])
		afterStart := rest[start+3:]
		end := strings.Index(afterStart, "}}")
		if end == -1 {
			return "", newErrorf(ErrCodeParse, "unclosed expression in %q", s)
		}
		expr := strings.TrimSpace(afterStart[:end])
		rest = afterStart[end+2:]

		if m := matrixExprRe.FindStringSubmatch(expr); m != nil {
			key := m[1]
			val, ok := matrix[key]
			if !ok {
				return "", newErrorf(ErrCodeParse, "unknown matrix key %q in expression", key)
			}
			out.WriteString(val)
			continue
		}
		if m := hashFilesExprRe.FindStringSubmatch(expr); m != nil {
			pattern := m[1]
			if err := validateRelativePath(pattern); err != nil {
				return "", newErrorf(ErrCodeParse, "invalid hashFiles pattern %q: %v", pattern, err)
			}
			if _, err := path.Match(pattern, "x"); err != nil {
				return "", newErrorf(ErrCodeParse, "malformed glob %q in hashFiles", pattern)
			}
			if hashFn != nil {
				sum, err := hashFn(pattern)
				if err != nil {
					return "", err
				}
				out.WriteString(sum)
			}
			continue
		}
		return "", newErrorf(ErrCodeParse, "unsupported expression %q", expr)
	}
	return out.String(), nil
}
