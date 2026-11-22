package preset

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jdestef90/hfdownloader/internal/transfer"
)

// INIEntry represents a single key = value line inside a Preset section.
type INIEntry struct {
	Key   string
	Value string
}

// INISection represents a [name] Preset block and its key/value entries.
type INISection struct {
	Name    string
	Entries []INIEntry
}

// INIDocument represents parsed sections of a llama.cpp Router Preset INI file.
type INIDocument struct {
	Sections []INISection
}

// Draft represents an editable Preset before it is written to the Preset file.
type Draft struct {
	Alias         string
	ModelPath     string
	ContextWindow *int
	MMProjPath    string
	MTPPath       string
	Defaults      []INIEntry
}

// AliasExistsError indicates the target Preset file already contains a Preset with the same alias.
type AliasExistsError struct {
	Alias string
}

func (e *AliasExistsError) Error() string {
	return fmt.Sprintf("a Preset named %q already exists; confirm overwriting it", e.Alias)
}

var (
	reservedKeys = map[string]bool{
		"model":       true,
		"ctx-size":    true,
		"mmproj":      true,
		"model-draft": true,
		"spec-type":   true,
	}
	sectionPattern      = regexp.MustCompile(`^\s*\[([^\]]+)\]\s*$`)
	entryPattern        = regexp.MustCompile(`^\s*([^=;#][^=]*?)\s*=\s*(.*?)\s*$`)
	aliasNonSafePattern = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
	multiDashPattern    = regexp.MustCompile(`-+`)
	ggufSuffixPattern   = regexp.MustCompile(`(?i)-GGUF$`)
)

// ParseINI parses INI sections and entries, ignoring comments and blank lines.
func ParseINI(text string) INIDocument {
	var sections []INISection
	currentIdx := -1
	for _, line := range splitLines(text) {
		if m := sectionPattern.FindStringSubmatch(line); len(m) == 2 {
			sections = append(sections, INISection{Name: strings.TrimSpace(m[1])})
			currentIdx = len(sections) - 1
			continue
		}
		trimmed := strings.TrimSpace(line)
		if currentIdx < 0 || trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if m := entryPattern.FindStringSubmatch(line); len(m) == 3 {
			sections[currentIdx].Entries = append(sections[currentIdx].Entries, INIEntry{
				Key:   strings.TrimSpace(m[1]),
				Value: strings.TrimSpace(m[2]),
			})
		}
	}
	return INIDocument{Sections: sections}
}

// FindSection returns the named section, or the first section if alias is empty.
func FindSection(text string, alias string) (*INISection, bool) {
	doc := ParseINI(text)
	if len(doc.Sections) == 0 {
		return nil, false
	}
	if alias == "" {
		s := doc.Sections[0]
		return &s, true
	}
	for i := range doc.Sections {
		if doc.Sections[i].Name == alias {
			s := doc.Sections[i]
			return &s, true
		}
	}
	return nil, false
}

// SectionNames returns all Preset aliases present in an INI document in order.
func SectionNames(text string) []string {
	doc := ParseINI(text)
	names := make([]string, 0, len(doc.Sections))
	for _, s := range doc.Sections {
		names = append(names, s.Name)
	}
	return names
}

// CreateDraftOptions supplies inputs for generating a new Preset Draft.
type CreateDraftOptions struct {
	Repo               string
	QuantLabel         string
	ModelPath          string
	ContextWindow      *int
	MMProjPath         string
	MTPPath            string
	ExistingPresetText string
	DefaultsAlias      string
}

// CreateDraft builds a Preset Draft with a sanitized alias prefilled from repo and Quant label.
func CreateDraft(opts CreateDraftOptions) (Draft, error) {
	modelPath, err := validatePath(opts.ModelPath, "model path")
	if err != nil {
		return Draft{}, err
	}
	if err := validateContextWindow(opts.ContextWindow); err != nil {
		return Draft{}, err
	}

	var defaults []INIEntry
	if opts.ExistingPresetText != "" && opts.DefaultsAlias != "" {
		if sec, ok := FindSection(opts.ExistingPresetText, opts.DefaultsAlias); ok {
			defaults = defaultsFromSection(sec)
		}
	}

	var ctxCopy *int
	if opts.ContextWindow != nil {
		v := *opts.ContextWindow
		ctxCopy = &v
	}

	d := Draft{
		Alias:         aliasFrom(opts.Repo, opts.QuantLabel),
		ModelPath:     modelPath,
		ContextWindow: ctxCopy,
		Defaults:      defaults,
	}
	if strings.TrimSpace(opts.MMProjPath) != "" {
		p, err := validatePath(opts.MMProjPath, "mmproj path")
		if err != nil {
			return Draft{}, err
		}
		d.MMProjPath = p
	}
	if strings.TrimSpace(opts.MTPPath) != "" {
		p, err := validatePath(opts.MTPPath, "mtp path")
		if err != nil {
			return Draft{}, err
		}
		d.MTPPath = p
	}
	return d, nil
}

// CopyDefaults copies non-reserved settings from an existing Preset section into the Draft.
func CopyDefaults(d Draft, presetText string, alias string) (Draft, error) {
	sec, ok := FindSection(presetText, alias)
	if !ok {
		if alias != "" {
			return Draft{}, fmt.Errorf("no Preset named %q was found", alias)
		}
		return Draft{}, errors.New("the Preset file has no existing Presets")
	}
	d.Defaults = defaultsFromSection(sec)
	return d, nil
}

// Render formats a Preset Draft into a single-section INI block.
func Render(d Draft) (string, error) {
	alias, err := validateAlias(d.Alias)
	if err != nil {
		return "", err
	}
	modelPath, err := validatePath(d.ModelPath, "model path")
	if err != nil {
		return "", err
	}
	if err := validateContextWindow(d.ContextWindow); err != nil {
		return "", err
	}

	entries := make([]INIEntry, 0, len(d.Defaults)+5)
	for _, e := range d.Defaults {
		if !reservedKeys[e.Key] {
			entries = append(entries, e)
		}
	}
	setEntry := func(key, value string) {
		for i := range entries {
			if entries[i].Key == key {
				entries[i].Value = value
				return
			}
		}
		entries = append(entries, INIEntry{Key: key, Value: value})
	}

	setEntry("model", modelPath)
	if d.ContextWindow != nil {
		setEntry("ctx-size", strconv.Itoa(*d.ContextWindow))
	}
	if strings.TrimSpace(d.MMProjPath) != "" {
		p, err := validatePath(d.MMProjPath, "mmproj path")
		if err != nil {
			return "", err
		}
		setEntry("mmproj", p)
	}
	if strings.TrimSpace(d.MTPPath) != "" {
		p, err := validatePath(d.MTPPath, "mtp path")
		if err != nil {
			return "", err
		}
		setEntry("model-draft", p)
		setEntry("spec-type", "draft-mtp")
	}

	var b strings.Builder
	b.WriteString("[")
	b.WriteString(alias)
	b.WriteString("]\n")
	for _, e := range entries {
		b.WriteString(e.Key)
		b.WriteString(" = ")
		b.WriteString(e.Value)
		b.WriteString("\n")
	}
	return b.String(), nil
}

// ApplyRaw parses edited single-section raw INI text back into a validated Draft.
func ApplyRaw(raw string) (Draft, error) {
	doc := ParseINI(raw)
	if len(doc.Sections) != 1 {
		return Draft{}, errors.New("raw INI view must contain exactly one Preset section")
	}
	sec := doc.Sections[0]
	alias, err := validateAlias(sec.Name)
	if err != nil {
		return Draft{}, err
	}

	values := make(map[string]string, len(sec.Entries))
	for _, e := range sec.Entries {
		values[e.Key] = e.Value
	}

	modelPath, err := validatePath(values["model"], "model path")
	if err != nil {
		return Draft{}, err
	}

	var ctxWin *int
	if rawCtx, ok := values["ctx-size"]; ok && strings.TrimSpace(rawCtx) != "" {
		v, convErr := strconv.Atoi(strings.TrimSpace(rawCtx))
		if convErr != nil {
			return Draft{}, errors.New("Context Window (ctx-size) must be a positive whole number")
		}
		ctxWin = &v
		if err := validateContextWindow(ctxWin); err != nil {
			return Draft{}, err
		}
	}

	d := Draft{
		Alias:         alias,
		ModelPath:     modelPath,
		ContextWindow: ctxWin,
		Defaults:      defaultsFromSection(&sec),
	}
	if mmproj := strings.TrimSpace(values["mmproj"]); mmproj != "" {
		p, err := validatePath(mmproj, "mmproj path")
		if err != nil {
			return Draft{}, err
		}
		d.MMProjPath = p
	}
	if mtp := strings.TrimSpace(values["model-draft"]); mtp != "" {
		p, err := validatePath(mtp, "mtp path")
		if err != nil {
			return Draft{}, err
		}
		d.MTPPath = p
	}
	return d, nil
}

// Upsert inserts or replaces the [alias] section inside an existing Preset INI file,
// preserving comments and all other Preset sections.
func Upsert(existing, alias, sectionText string) (string, error) {
	replacement, err := extractSectionBlock(sectionText, alias)
	if err != nil {
		return "", err
	}

	var lines []string
	if existing != "" {
		lines = splitLines(existing)
	}

	start := -1
	end := len(lines)
	for i, line := range lines {
		m := sectionPattern.FindStringSubmatch(line)
		if len(m) != 2 {
			continue
		}
		if start == -1 && strings.TrimSpace(m[1]) == alias {
			start = i
			continue
		}
		if start != -1 {
			end = i
			break
		}
	}

	if start == -1 {
		for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, replacement...)
	} else {
		// Preserve trailing blank line separating sections if the next section follows.
		tail := append([]string(nil), lines[end:]...)
		updated := append([]string(nil), lines[:start]...)
		updated = append(updated, replacement...)
		if end < len(lines) {
			updated = append(updated, "")
		}
		lines = append(updated, tail...)
	}

	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// WriteOptions configures writing a Preset Draft to a Target.
type WriteOptions struct {
	Target         transfer.Target
	PresetPath     string
	Draft          Draft
	RawText        string
	OverwriteAlias bool
	Now            func() time.Time
}

// WriteResult describes the written Preset file and optional backup path.
type WriteResult struct {
	BackupPath string
	Content    string
}

// Write backs up an existing Preset file with a timestamp and writes the updated Preset.
func Write(ctx context.Context, opts WriteOptions) (WriteResult, error) {
	if opts.Target == nil {
		return WriteResult{}, errors.New("target is required to write Preset")
	}
	alias, err := validateAlias(opts.Draft.Alias)
	if err != nil {
		return WriteResult{}, err
	}

	existing, exists, err := opts.Target.ReadText(ctx, opts.PresetPath)
	if err != nil {
		return WriteResult{}, err
	}

	if exists {
		doc := ParseINI(existing)
		for _, sec := range doc.Sections {
			if sec.Name == alias && !opts.OverwriteAlias {
				return WriteResult{}, &AliasExistsError{Alias: alias}
			}
		}
	}

	sectionText := opts.RawText
	if strings.TrimSpace(sectionText) == "" {
		sectionText, err = Render(opts.Draft)
		if err != nil {
			return WriteResult{}, err
		}
	}

	content, err := Upsert(existing, alias, sectionText)
	if err != nil {
		return WriteResult{}, err
	}

	var backupPath string
	if exists {
		nowFn := opts.Now
		if nowFn == nil {
			nowFn = time.Now
		}
		stamp := nowFn().UTC().Format("2006-01-02T15-04-05.000Z")
		backupPath = fmt.Sprintf("%s.bak-%s", opts.PresetPath, stamp)
		if err := opts.Target.WriteText(ctx, backupPath, existing); err != nil {
			return WriteResult{}, fmt.Errorf("failed to write Preset backup %s: %w", backupPath, err)
		}
	}

	if err := opts.Target.WriteText(ctx, opts.PresetPath, content); err != nil {
		return WriteResult{}, fmt.Errorf("failed to write Preset file %s: %w", opts.PresetPath, err)
	}

	return WriteResult{
		BackupPath: backupPath,
		Content:    content,
	}, nil
}

func defaultsFromSection(sec *INISection) []INIEntry {
	if sec == nil {
		return nil
	}
	out := make([]INIEntry, 0, len(sec.Entries))
	for _, e := range sec.Entries {
		if !reservedKeys[e.Key] {
			out = append(out, e)
		}
	}
	return out
}

func sanitizeAlias(v string) string {
	s := aliasNonSafePattern.ReplaceAllString(v, "-")
	s = multiDashPattern.ReplaceAllString(s, "-")
	return strings.Trim(s, "-.")
}

func aliasFrom(repo, quantLabel string) string {
	repoName := ggufSuffixPattern.ReplaceAllString(path.Base(repo), "")
	base := sanitizeAlias(repoName)
	if base == "" {
		base = "model"
	}
	quant := sanitizeAlias(quantLabel)
	if quant == "" {
		return base
	}
	return base + "-" + quant
}

func validateAlias(alias string) (string, error) {
	trimmed := strings.TrimSpace(alias)
	if trimmed == "" {
		return "", errors.New("Preset alias cannot be empty")
	}
	if strings.ContainsAny(trimmed, "[]\r\n") {
		return "", errors.New("Preset alias cannot contain brackets or newlines")
	}
	return trimmed, nil
}

func validateContextWindow(v *int) error {
	if v == nil {
		return nil
	}
	if *v <= 0 {
		return errors.New("Context Window must be a positive whole number")
	}
	return nil
}

func validatePath(v, label string) (string, error) {
	trimmed := strings.TrimSpace(v)
	if trimmed == "" {
		return "", fmt.Errorf("%s cannot be empty", label)
	}
	if strings.ContainsAny(trimmed, "\r\n") {
		return "", fmt.Errorf("%s cannot contain newlines", label)
	}
	return trimmed, nil
}

func splitLines(text string) []string {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	return strings.Split(normalized, "\n")
}

func extractSectionBlock(text, alias string) ([]string, error) {
	lines := splitLines(text)
	start := -1
	end := len(lines)
	for i, line := range lines {
		if m := sectionPattern.FindStringSubmatch(line); len(m) == 2 {
			if start == -1 {
				start = i
			} else {
				end = i
				break
			}
		}
	}
	if start == -1 {
		return nil, errors.New("Preset section header is missing")
	}
	block := append([]string(nil), lines[start:end]...)
	for len(block) > 0 && strings.TrimSpace(block[len(block)-1]) == "" {
		block = block[:len(block)-1]
	}
	block[0] = "[" + alias + "]"
	return block, nil
}
