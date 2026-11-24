package paperwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

const (
	MaxCanonicalBytes        = 16 << 20
	MaxJSONTokens            = 262144
	MaxJSONDepth             = 16
	MaxArrayItems            = 65536
	MaxConfigBytes           = 64 << 10
	MaxPlanBytes             = 1 << 20
	MaxObservationPages      = 8192
	MaxObservationRows       = 65536
	MaxActualFillRecords     = 256
	MaxExpectedFundingEvents = 4096
)

var (
	ErrPaperWire          = errors.New("invalid paper wire record")
	ErrPaperResourceLimit = errors.New("paper resource limit exceeded")
)

type frame struct {
	kind  json.Delim
	items int
}

// preflight ограничивает JSON до выделения типизированных массивов и embedded payloads.
func preflight(raw []byte) error {
	if len(raw) == 0 {
		return ErrPaperWire
	}
	if len(raw) > MaxCanonicalBytes {
		return errors.Join(ErrPaperWire, ErrPaperResourceLimit)
	}
	if !utf8.Valid(raw) {
		return ErrPaperWire
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var stack []frame
	seen := false
	for tokens := 0; ; tokens++ {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			if len(stack) != 0 || !seen {
				return ErrPaperWire
			}
			return nil
		}
		if err != nil {
			return ErrPaperWire
		}
		if tokens >= MaxJSONTokens {
			return errors.Join(ErrPaperWire, ErrPaperResourceLimit)
		}
		delimiter, isDelimiter := token.(json.Delim)
		closing := isDelimiter && (delimiter == ']' || delimiter == '}')
		if len(stack) == 0 && !closing {
			if seen {
				return ErrPaperWire
			}
			seen = true
		}
		if !closing && len(stack) > 0 && stack[len(stack)-1].kind == '[' {
			stack[len(stack)-1].items++
			if stack[len(stack)-1].items > MaxArrayItems {
				return errors.Join(ErrPaperWire, ErrPaperResourceLimit)
			}
		}
		if isDelimiter {
			if closing {
				stack = stack[:len(stack)-1]
			} else {
				stack = append(stack, frame{kind: delimiter})
				if len(stack) > MaxJSONDepth {
					return errors.Join(ErrPaperWire, ErrPaperResourceLimit)
				}
			}
		}
	}
}

type field struct {
	name  string
	value any
}

// encodeFields сохраняет явно заданный порядок ключей без reflection framework.
func encodeFields(fields []field) ([]byte, error) {
	var out bytes.Buffer
	out.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			out.WriteByte(',')
		}
		key, _ := json.Marshal(f.name)
		value, err := json.Marshal(f.value)
		if err != nil {
			return nil, errors.Join(ErrPaperWire, err)
		}
		out.Write(key)
		out.WriteByte(':')
		out.Write(value)
	}
	out.WriteByte('}')
	if out.Len() > MaxCanonicalBytes {
		return nil, errors.Join(ErrPaperWire, ErrPaperResourceLimit)
	}
	return out.Bytes(), nil
}

// decodeFields требует все известные поля; каноничность проверяет вызывающий re-encode.
func decodeFields(raw []byte, fields []field) error {
	if err := preflight(raw); err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || len(object) != len(fields) {
		return ErrPaperWire
	}
	for _, f := range fields {
		value, ok := object[f.name]
		if !ok {
			return ErrPaperWire
		}
		if err := json.Unmarshal(value, f.value); err != nil {
			return errors.Join(ErrPaperWire, err)
		}
	}
	return nil
}
