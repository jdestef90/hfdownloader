package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/jdestef90/hfdownloader/internal/preset"
)

type presetField int

const (
	fieldAlias presetField = iota
	fieldModelPath
	fieldContextWindow
	fieldCount
)

type presetEditor struct {
	draft       preset.Draft
	rawMode     bool
	activeField presetField

	aliasInput textinput.Model
	modelInput textinput.Model
	ctxInput   textinput.Model
	rawArea    textarea.Model

	existingText   string
	existingNames  []string
	defaultsCursor int
	copiedFrom     string
	ctxSourceLabel string
}

func newPresetEditor(draft preset.Draft, existingText string, ctxSourceLabel string) presetEditor {
	aliasIn := textinput.New()
	aliasIn.Placeholder = "model-alias"
	aliasIn.CharLimit = 128
	aliasIn.Width = 56
	aliasIn.SetValue(draft.Alias)
	aliasIn.Focus()

	modelIn := textinput.New()
	modelIn.Placeholder = "/path/to/model.gguf"
	modelIn.CharLimit = 512
	modelIn.Width = 56
	modelIn.SetValue(draft.ModelPath)

	ctxIn := textinput.New()
	ctxIn.Placeholder = "e.g. 32768 (optional)"
	ctxIn.CharLimit = 16
	ctxIn.Width = 24
	if draft.ContextWindow != nil {
		ctxIn.SetValue(strconv.Itoa(*draft.ContextWindow))
	}

	raw := textarea.New()
	raw.SetWidth(68)
	raw.SetHeight(12)
	raw.ShowLineNumbers = true
	if rendered, err := preset.Render(draft); err == nil {
		raw.SetValue(rendered)
	}

	return presetEditor{
		draft:          draft,
		activeField:    fieldAlias,
		aliasInput:     aliasIn,
		modelInput:     modelIn,
		ctxInput:       ctxIn,
		rawArea:        raw,
		existingText:   existingText,
		existingNames:  preset.SectionNames(existingText),
		ctxSourceLabel: ctxSourceLabel,
	}
}

func (e *presetEditor) setWidth(totalWidth int) {
	w := totalWidth - 16
	if w > 76 {
		w = 76
	}
	if w < 30 {
		w = 30
	}
	e.aliasInput.Width = w
	e.modelInput.Width = w
	e.rawArea.SetWidth(w + 6)
}

func (e *presetEditor) syncFromStructured() (preset.Draft, error) {
	d := e.draft
	d.Alias = strings.TrimSpace(e.aliasInput.Value())
	d.ModelPath = strings.TrimSpace(e.modelInput.Value())

	rawCtx := strings.TrimSpace(e.ctxInput.Value())
	if rawCtx == "" {
		d.ContextWindow = nil
	} else {
		v, err := strconv.Atoi(rawCtx)
		if err != nil || v <= 0 {
			return preset.Draft{}, fmt.Errorf("Context Window must be a positive whole number")
		}
		d.ContextWindow = &v
	}

	if _, err := preset.Render(d); err != nil {
		return preset.Draft{}, err
	}
	e.draft = d
	return d, nil
}

func (e *presetEditor) syncToStructured(d preset.Draft) {
	e.draft = d
	e.aliasInput.SetValue(d.Alias)
	e.modelInput.SetValue(d.ModelPath)
	if d.ContextWindow != nil {
		e.ctxInput.SetValue(strconv.Itoa(*d.ContextWindow))
	} else {
		e.ctxInput.SetValue("")
	}
}

func (e *presetEditor) toggleRawMode() error {
	if !e.rawMode {
		d, err := e.syncFromStructured()
		if err != nil {
			return err
		}
		rendered, err := preset.Render(d)
		if err != nil {
			return err
		}
		e.rawArea.SetValue(rendered)
		e.blurStructured()
		e.rawArea.Focus()
		e.rawMode = true
		return nil
	}

	d, err := preset.ApplyRaw(e.rawArea.Value())
	if err != nil {
		return err
	}
	e.syncToStructured(d)
	e.rawArea.Blur()
	e.rawMode = false
	e.focusActiveField()
	return nil
}

func (e *presetEditor) buildForSave() (preset.Draft, string, error) {
	if e.rawMode {
		rawText := e.rawArea.Value()
		d, err := preset.ApplyRaw(rawText)
		if err != nil {
			return preset.Draft{}, "", err
		}
		e.syncToStructured(d)
		return d, rawText, nil
	}
	d, err := e.syncFromStructured()
	if err != nil {
		return preset.Draft{}, "", err
	}
	rendered, err := preset.Render(d)
	if err != nil {
		return preset.Draft{}, "", err
	}
	e.rawArea.SetValue(rendered)
	return d, rendered, nil
}

func (e *presetEditor) applyCopiedDefaults(alias string) error {
	base := e.draft
	if e.rawMode {
		parsed, err := preset.ApplyRaw(e.rawArea.Value())
		if err == nil {
			base = parsed
		}
	} else {
		if synced, err := e.syncFromStructured(); err == nil {
			base = synced
		}
	}
	updated, err := preset.CopyDefaults(base, e.existingText, alias)
	if err != nil {
		return err
	}
	e.syncToStructured(updated)
	if rendered, err := preset.Render(updated); err == nil {
		e.rawArea.SetValue(rendered)
	}
	e.copiedFrom = alias
	return nil
}

func (e *presetEditor) blurStructured() {
	e.aliasInput.Blur()
	e.modelInput.Blur()
	e.ctxInput.Blur()
}

func (e *presetEditor) focusActiveField() {
	e.blurStructured()
	switch e.activeField {
	case fieldAlias:
		e.aliasInput.Focus()
	case fieldModelPath:
		e.modelInput.Focus()
	case fieldContextWindow:
		e.ctxInput.Focus()
	}
}

func (e *presetEditor) nextField() {
	e.activeField = (e.activeField + 1) % fieldCount
	e.focusActiveField()
}

func (e *presetEditor) prevField() {
	e.activeField = (e.activeField + fieldCount - 1) % fieldCount
	e.focusActiveField()
}

func (e *presetEditor) updateInput(msg tea.Msg) tea.Cmd {
	if e.rawMode {
		var cmd tea.Cmd
		e.rawArea, cmd = e.rawArea.Update(msg)
		return cmd
	}
	var cmd tea.Cmd
	switch e.activeField {
	case fieldAlias:
		e.aliasInput, cmd = e.aliasInput.Update(msg)
	case fieldModelPath:
		e.modelInput, cmd = e.modelInput.Update(msg)
	case fieldContextWindow:
		e.ctxInput, cmd = e.ctxInput.Update(msg)
	}
	return cmd
}
