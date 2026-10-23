// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

var actionSymbol = map[Action]string{ //nolint:gochecknoglobals // immutable lookup table
	ActionCreate: "+", ActionUpdate: "~", ActionDelete: "-", ActionUnchanged: "=",
}

// WriteDiff prints the plan as a field-level diff: one header per changed
// object, then "-" (live) and "+" (desired) lines per field. Unchanged objects
// are omitted.
func WriteDiff(w io.Writer, plan *Plan) {
	for _, it := range plan.Items {
		if it.Action == ActionUnchanged && it.Err == nil {
			continue
		}

		fmt.Fprintf(w, "%s %s (%s)\n", actionSymbol[it.Action], Ident(it.Kind, it.Key), describe(it))

		if it.Err != nil {
			fmt.Fprintf(w, "!   error: %s\n", it.Err)
		}

		if it.Action == ActionDelete {
			continue
		}

		for _, c := range it.Changes {
			writeChange(w, it.Action, c)
		}
	}
}

func describe(it *Item) string {
	switch {
	case it.Action == ActionDelete && it.Managed:
		return "delete, managed by oa apply"
	case it.Action == ActionDelete:
		return "delete, not managed (--prune-all)"
	case it.Action == ActionUnchanged && it.Err != nil:
		return "error"
	}

	return string(it.Action)
}

func writeChange(w io.Writer, action Action, c Change) {
	if c.Secret {
		fmt.Fprintf(w, "+   %s: (secret, will be sent)\n", c.Path)
		return
	}

	switch {
	case action == ActionCreate || (c.Old == nil && c.New != nil):
		fmt.Fprintf(w, "+   %s: %s\n", c.Path, FormatValue(c.New))
	case c.New == nil && strings.Contains(c.Path, "[") && c.Old != nil:
		fmt.Fprintf(w, "-   %s: %s\n", c.Path, FormatValue(c.Old))
	default:
		fmt.Fprintf(w, "-   %s: %s\n", c.Path, FormatValue(c.Old))
		fmt.Fprintf(w, "+   %s: %s\n", c.Path, FormatValue(c.New))
	}
}

// WriteTable prints a one-line-per-object summary. With outcomes, the RESULT
// column shows what happened to each item.
func WriteTable(w io.Writer, plan *Plan, outcomes []Outcome) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	byItem := map[*Item]Outcome{}
	for _, o := range outcomes {
		byItem[o.Item] = o
	}

	header := "ACTION\tKIND\tNAME\tDETAIL"
	if outcomes != nil {
		header += "\tRESULT"
	}

	fmt.Fprintln(tw, header)

	for _, it := range plan.Items {
		row := fmt.Sprintf("%s\t%s\t%s\t%s", it.Action, it.Kind, it.Key, detail(it))

		if outcomes != nil {
			row += "\t" + resultText(byItem[it])
		}

		fmt.Fprintln(tw, row)
	}

	_ = tw.Flush()
}

func detail(it *Item) string {
	switch {
	case it.Err != nil:
		return "error: " + it.Err.Error()
	case it.Action == ActionCreate:
		return fmt.Sprintf("%d field(s)", len(it.Changes))
	case it.Action == ActionUpdate:
		paths := make([]string, len(it.Changes))
		for i, c := range it.Changes {
			paths[i] = c.Path
		}

		return strings.Join(paths, ", ")
	case it.Action == ActionDelete && !it.Managed:
		return "not managed"
	case it.Action == ActionUnchanged && it.Adopt:
		return "adopt (record ownership)"
	}

	return ""
}

func resultText(o Outcome) string {
	switch o.Status {
	case StatusFailed:
		return "FAILED: " + o.Err.Error()
	case StatusSkipped:
		return "skipped"
	case StatusApplied:
		return "ok"
	}

	return ""
}

// WriteSummary prints the one-line totals.
func WriteSummary(w io.Writer, plan *Plan) {
	c := plan.Counts()
	fmt.Fprintf(w, "Plan: %d to create, %d to update, %d to delete, %d unchanged.\n",
		c[ActionCreate], c[ActionUpdate], c[ActionDelete], c[ActionUnchanged])
}

type jsonChange struct {
	Path   string `json:"path"`
	Old    any    `json:"old,omitempty"`
	New    any    `json:"new,omitempty"`
	Secret bool   `json:"secret,omitempty"`
}

type jsonItem struct {
	Action  Action       `json:"action"`
	Kind    string       `json:"kind"`
	Name    string       `json:"name"`
	Changes []jsonChange `json:"changes,omitempty"`
	Adopt   bool         `json:"adopt,omitempty"`
	Managed bool         `json:"managed,omitempty"`
	Result  Status       `json:"result,omitempty"`
	Error   string       `json:"error,omitempty"`
}

// JSONReport is the machine-readable form of a plan (and its execution).
type JSONReport struct {
	Summary map[Action]int `json:"summary"`
	Items   []jsonItem     `json:"items"`
	Changes bool           `json:"changes"`
	Errors  []string       `json:"errors,omitempty"`
}

// BuildJSON converts a plan (and optional outcomes) into a JSONReport. Secret
// values never appear: secret changes carry only the flag.
func BuildJSON(plan *Plan, outcomes []Outcome) *JSONReport {
	rep := &JSONReport{Summary: plan.Counts(), Changes: plan.HasChanges(), Items: make([]jsonItem, 0, len(plan.Items))}

	byItem := map[*Item]Outcome{}
	for _, o := range outcomes {
		byItem[o.Item] = o
	}

	for _, it := range plan.Items {
		ji := jsonItem{Action: it.Action, Kind: it.Kind, Name: it.Key, Adopt: it.Adopt, Managed: it.Managed}

		for _, c := range it.Changes {
			jc := jsonChange{Path: c.Path, Secret: c.Secret}
			if !c.Secret {
				jc.Old, jc.New = jsonSafe(c.Old), jsonSafe(c.New)
			}

			ji.Changes = append(ji.Changes, jc)
		}

		if it.Err != nil {
			ji.Error = it.Err.Error()
			rep.Errors = append(rep.Errors, fmt.Sprintf("%s: %s", Ident(it.Kind, it.Key), it.Err))
		}

		if o, ok := byItem[it]; ok {
			ji.Result = o.Status
			if o.Err != nil {
				ji.Error = o.Err.Error()
			}
		}

		rep.Items = append(rep.Items, ji)
	}

	return rep
}

// WriteJSON prints BuildJSON(plan, outcomes) as indented JSON.
func WriteJSON(w io.Writer, plan *Plan, outcomes []Outcome) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")

	if err := enc.Encode(BuildJSON(plan, outcomes)); err != nil {
		return fmt.Errorf("encode report: %w", err)
	}

	return nil
}

func jsonSafe(v any) any {
	switch t := v.(type) {
	case SecretRef:
		return "(secret)"
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = jsonSafe(e)
		}

		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = jsonSafe(e)
		}

		return out
	}

	return v
}
