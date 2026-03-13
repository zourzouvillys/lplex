package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sixfathoms/lplex"
	"github.com/sixfathoms/lplex/filter"
	"github.com/sixfathoms/lplex/lplexc"
	"github.com/sixfathoms/lplex/pgn"
)

func changeTag(ct lplex.ChangeEventType, diffBytes, fullBytes int) string {
	switch ct {
	case lplex.Snapshot:
		return ansiGreen + "[snapshot]" + ansiReset + " "
	case lplex.Delta:
		return fmt.Sprintf("%s[delta %d/%dB]%s ", ansiYellow, diffBytes, fullBytes, ansiReset)
	case lplex.Idle:
		return ansiDim + "[idle]    " + ansiReset + " "
	default:
		return ""
	}
}

func formatFrame(w *bufio.Writer, f *lplexc.Frame, dm *deviceMap, decode bool, preDecoded any, ct lplex.ChangeEventType, diffBytes, fullBytes int) {
	ts := f.Ts
	if t, err := time.Parse(time.RFC3339Nano, f.Ts); err == nil {
		ts = t.Local().Format("15:04:05.000")
	}

	srcLabel := fmt.Sprintf("[%d]", f.Src)
	if d, ok := dm.get(f.Src); ok && d.Manufacturer != "" {
		srcLabel = fmt.Sprintf("%s(%d)[%d]", d.Manufacturer, d.ManufacturerCode, f.Src)
	}

	var pgnName string
	if info, ok := pgn.Registry[f.PGN]; ok {
		pgnName = info.Description
	}
	sc := colorForSrc(f.Src)

	tag := changeTag(ct, diffBytes, fullBytes)
	fmt.Fprintf(w, "%s%s%s %s%s#%-7d%s %s%-20s%s %s>%02x%s  %s%s%-6d%s",
		ansiDim, ts, ansiReset,
		tag,
		ansiDim, f.Seq, ansiReset,
		sc+ansiBold, srcLabel, ansiReset,
		ansiDim, f.Dst, ansiReset,
		ansiCyan, ansiBold, f.PGN, ansiReset,
	)
	if pgnName != "" {
		fmt.Fprintf(w, " %s%-22s%s", ansiCyan, pgnName, ansiReset)
	} else {
		w.WriteString("                       ")
	}
	if !decode {
		fmt.Fprintf(w, " %sp%d%s  %s\n",
			ansiDim, f.Prio, ansiReset,
			f.Data,
		)
		return
	}
	// Use pre-decoded value if available, otherwise decode fresh.
	var decoded any
	if preDecoded != nil {
		decoded = withLookupNames(preDecoded)
	} else {
		var err error
		decoded, err = decodeFrame(f)
		if err != nil {
			fmt.Fprintf(w, " %sp%d%s  %s  %s%s%s\n",
				ansiDim, f.Prio, ansiReset,
				f.Data,
				ansiRed, err.Error(), ansiReset,
			)
			return
		}
	}
	if decoded == nil {
		fmt.Fprintf(w, " %sp%d%s  %s\n",
			ansiDim, f.Prio, ansiReset,
			f.Data,
		)
		return
	}
	b, _ := json.Marshal(decoded)
	fmt.Fprintf(w, " %sp%d%s  %s%s%s\n",
		ansiDim, f.Prio, ansiReset,
		ansiDim, string(b), ansiReset,
	)
}

// decodeFrame attempts to decode a frame's hex data using the pgn.Registry.
// If the decoded struct has lookup fields, the result is wrapped so those fields
// serialize as {"id": <value>, "name": "..."} objects instead of flat integers.
func decodeFrame(f *lplexc.Frame) (any, error) {
	info, ok := pgn.Registry[f.PGN]
	if !ok || info.Decode == nil {
		return nil, nil
	}
	data, err := hex.DecodeString(f.Data)
	if err != nil {
		return nil, err
	}
	v, err := info.Decode(data)
	if err != nil {
		return nil, err
	}
	return withLookupNames(v), nil
}

// decodeFrameRaw decodes a frame's hex data using the pgn.Registry, returning
// the raw decoded struct without lookup name wrapping.
func decodeFrameRaw(f *lplexc.Frame) (any, error) {
	info, ok := pgn.Registry[f.PGN]
	if !ok || info.Decode == nil {
		return nil, nil
	}
	data, err := hex.DecodeString(f.Data)
	if err != nil {
		return nil, err
	}
	return info.Decode(data)
}

// matchesDisplayFilter evaluates a display filter against a frame with optional decoded data.
// The deviceMap is used to resolve src/dst sub-accessors (e.g., src.manufacturer, dst.manufacturer).
func matchesDisplayFilter(df *filter.Filter, f *lplexc.Frame, decoded any, dm *deviceMap) bool {
	if df == nil {
		return true
	}
	ctx := &filter.EvalContext{PGN: f.PGN, Src: f.Src, Dst: f.Dst, Prio: f.Prio, Decoded: decoded}
	if lf, ok := decoded.(lookupFielder); ok {
		ctx.Lookups = lf.LookupFields()
	}
	if dm != nil {
		if ctx.Lookups == nil {
			ctx.Lookups = make(map[string]string)
		}
		if d, ok := dm.get(f.Src); ok {
			ctx.Lookups["src.manufacturer"] = d.Manufacturer
			ctx.Lookups["src.model_id"] = d.ModelID
			ctx.Lookups["src.name"] = d.Name
		}
		if d, ok := dm.get(f.Dst); ok {
			ctx.Lookups["dst.manufacturer"] = d.Manufacturer
			ctx.Lookups["dst.model_id"] = d.ModelID
			ctx.Lookups["dst.name"] = d.Name
		}
	}
	return df.Match(ctx)
}

// lookupFielder is implemented by generated PGN structs that have lookup= fields.
type lookupFielder interface {
	LookupFields() map[string]string
}

// withLookupNames wraps a decoded PGN value so lookup fields serialize as
// {"id": <raw>, "name": "..."} objects instead of flat integers.
func withLookupNames(v any) any {
	lf, ok := v.(lookupFielder)
	if !ok {
		return v
	}
	fields := lf.LookupFields()
	if len(fields) == 0 {
		return v
	}
	return &annotatedDecoded{value: v, fields: fields}
}

// annotatedDecoded wraps a decoded PGN struct, transforming lookup fields from flat
// integers into {"id": <value>} or {"id": <value>, "name": "..."} objects.
type annotatedDecoded struct {
	value  any
	fields map[string]string // JSON field name -> resolved lookup name ("" if unknown)
}

func (a *annotatedDecoded) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(a.value)
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return b, nil
	}
	type lookupObj struct {
		ID   json.RawMessage `json:"id"`
		Name string          `json:"name,omitempty"`
	}
	for jsonKey, name := range a.fields {
		val, ok := raw[jsonKey]
		if !ok {
			continue
		}
		obj, _ := json.Marshal(lookupObj{ID: val, Name: name})
		raw[jsonKey] = obj
	}
	return json.Marshal(raw)
}

// writeJSONFrame writes a frame as a JSON line, optionally with decoded fields.
func writeJSONFrame(w *bufio.Writer, f *lplexc.Frame, decode bool, preDecoded any, ct lplex.ChangeEventType, diffBytes, fullBytes int) {
	if !decode && ct == 0 {
		fmt.Fprintf(w, "{\"seq\":%d,\"ts\":\"%s\",\"prio\":%d,\"pgn\":%d,\"src\":%d,\"dst\":%d,\"data\":\"%s\"}\n",
			f.Seq, f.Ts, f.Prio, f.PGN, f.Src, f.Dst, f.Data)
		return
	}
	type jsonFrame struct {
		Change      string `json:"change,omitempty"`
		DiffBytes   int    `json:"diff_bytes,omitempty"`
		FullBytes   int    `json:"full_bytes,omitempty"`
		Seq         uint64 `json:"seq"`
		Ts          string `json:"ts"`
		Prio        uint8  `json:"prio"`
		PGN         uint32 `json:"pgn"`
		Src         uint8  `json:"src"`
		Dst         uint8  `json:"dst"`
		Data        string `json:"data"`
		Decoded     any    `json:"decoded,omitempty"`
		DecodeError string `json:"decode_error,omitempty"`
	}
	jf := jsonFrame{
		Seq: f.Seq, Ts: f.Ts, Prio: f.Prio,
		PGN: f.PGN, Src: f.Src, Dst: f.Dst, Data: f.Data,
	}
	if ct != 0 {
		jf.Change = ct.String()
	}
	if ct == lplex.Delta {
		jf.DiffBytes = diffBytes
		jf.FullBytes = fullBytes
	}
	if decode {
		if preDecoded != nil {
			jf.Decoded = withLookupNames(preDecoded)
		} else if v, err := decodeFrame(f); err != nil {
			jf.DecodeError = err.Error()
		} else if v != nil {
			jf.Decoded = v
		}
	}
	b, _ := json.Marshal(jf)
	_, _ = w.Write(b)
	_ = w.WriteByte('\n')
}

func formatIdleEvent(w *bufio.Writer, ev lplex.ChangeEvent, dm *deviceMap) {
	ts := ev.Timestamp.Local().Format("15:04:05.000")

	srcLabel := fmt.Sprintf("[%d]", ev.Source)
	if d, ok := dm.get(ev.Source); ok && d.Manufacturer != "" {
		srcLabel = fmt.Sprintf("%s(%d)[%d]", d.Manufacturer, d.ManufacturerCode, ev.Source)
	}

	var pgnName string
	if info, ok := pgn.Registry[ev.PGN]; ok {
		pgnName = info.Description
	}
	sc := colorForSrc(ev.Source)

	fmt.Fprintf(w, "%s%s%s %s[idle]%s    %s%-20s%s  %s%s%-6d%s",
		ansiDim, ts, ansiReset,
		ansiDim, ansiReset,
		sc+ansiBold, srcLabel, ansiReset,
		ansiCyan, ansiBold, ev.PGN, ansiReset,
	)
	if pgnName != "" {
		fmt.Fprintf(w, " %s%s%s", ansiCyan, pgnName, ansiReset)
	}
	_ = w.WriteByte('\n')
}

func writeJSONIdleEvent(w *bufio.Writer, ev lplex.ChangeEvent) {
	type jsonIdle struct {
		Change string `json:"change"`
		Ts     string `json:"ts"`
		PGN    uint32 `json:"pgn"`
		Src    uint8  `json:"src"`
	}
	b, _ := json.Marshal(jsonIdle{
		Change: "idle",
		Ts:     ev.Timestamp.UTC().Format(time.RFC3339Nano),
		PGN:    ev.PGN,
		Src:    ev.Source,
	})
	_, _ = w.Write(b)
	_ = w.WriteByte('\n')
}
