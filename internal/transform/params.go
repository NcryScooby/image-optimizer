package transform

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Params holds image transform options shared across packages.
type Params struct {
	W    *int   `json:"w,omitempty"`
	H    *int   `json:"h,omitempty"`
	Crop string `json:"crop,omitempty"`
	Fit  string `json:"fit,omitempty"`
	Q    int    `json:"q"`
}

const defaultQuality = 80

var (
	validCrops = map[string]struct{}{
		"center": {}, "top": {}, "bottom": {}, "left": {}, "right": {},
	}
	validFits = map[string]struct{}{
		"cover": {}, "contain": {}, "fill": {},
	}
)

// Parse validates and extracts transform params from URL query values.
// Missing keys yield zero values (identity variant before Normalize).
func Parse(q url.Values) (Params, error) {
	var p Params

	if v := q.Get("w"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return Params{}, fmt.Errorf("invalid w: must be int > 0")
		}
		p.W = &n
	}

	if v := q.Get("h"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return Params{}, fmt.Errorf("invalid h: must be int > 0")
		}
		p.H = &n
	}

	if v := q.Get("crop"); v != "" {
		if _, ok := validCrops[v]; !ok {
			return Params{}, fmt.Errorf("invalid crop: must be center|top|bottom|left|right")
		}
		p.Crop = v
	}

	if v := q.Get("fit"); v != "" {
		if _, ok := validFits[v]; !ok {
			return Params{}, fmt.Errorf("invalid fit: must be cover|contain|fill")
		}
		p.Fit = v
	}

	if v := q.Get("q"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return Params{}, fmt.Errorf("invalid q: must be int 1–100")
		}
		p.Q = n
	}

	return p, nil
}

// Normalize applies defaults: q=80; when resizing, crop=center and fit=cover if unset.
func Normalize(p Params) Params {
	out := p
	if out.Q == 0 {
		out.Q = defaultQuality
	}
	if out.W != nil || out.H != nil {
		if out.Crop == "" {
			out.Crop = "center"
		}
		if out.Fit == "" {
			out.Fit = "cover"
		}
	}
	return out
}

// Hash returns the SHA-256 hex digest of normalized, order-stable params.
func Hash(p Params) string {
	sum := sha256.Sum256([]byte(canonicalKey(Normalize(p))))
	return hex.EncodeToString(sum[:])
}

// CacheKeyJSON returns JSON of normalized params (for variants.params_json).
func CacheKeyJSON(p Params) []byte {
	b, err := json.Marshal(Normalize(p))
	if err != nil {
		// Params only contains primitives; marshal cannot fail in practice.
		return []byte("{}")
	}
	return b
}

// canonicalKey builds a stable, ordered string for hashing.
func canonicalKey(p Params) string {
	parts := make([]string, 0, 5)
	if p.Crop != "" {
		parts = append(parts, "crop="+p.Crop)
	}
	if p.Fit != "" {
		parts = append(parts, "fit="+p.Fit)
	}
	if p.H != nil {
		parts = append(parts, "h="+strconv.Itoa(*p.H))
	}
	parts = append(parts, "q="+strconv.Itoa(p.Q))
	if p.W != nil {
		parts = append(parts, "w="+strconv.Itoa(*p.W))
	}
	return strings.Join(parts, "&")
}
