package attestation

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ParsePCRFile parses the PCR file produced by the build (pcrs.json). It
// accepts a flat object {"PCR0": "<hex>", ...} and the nitro-cli shape
// {"Measurements": {"PCR0": "<hex>", ...}}. Non-PCR keys are ignored.
func ParsePCRFile(data []byte) (map[uint][]byte, error) {
	var outer map[string]json.RawMessage
	if err := json.Unmarshal(data, &outer); err != nil {
		return nil, fmt.Errorf("pcrs: %w", err)
	}
	if m, ok := outer["Measurements"]; ok {
		if err := json.Unmarshal(m, &outer); err != nil {
			return nil, fmt.Errorf("pcrs: Measurements: %w", err)
		}
	}
	pcrs := map[uint][]byte{}
	for k, v := range outer {
		idx, ok := strings.CutPrefix(k, "PCR")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(idx, 10, 8)
		if err != nil {
			continue
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return nil, fmt.Errorf("pcrs: %s: %w", k, err)
		}
		b, err := hex.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("pcrs: %s: %w", k, err)
		}
		pcrs[uint(n)] = b
	}
	for _, i := range MeasuredPCRs {
		if len(pcrs[i]) != 48 {
			return nil, fmt.Errorf("pcrs: PCR%d missing or not SHA-384", i)
		}
	}
	return pcrs, nil
}
