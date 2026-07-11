package transform

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"testing"
)

func TestParse_EmptyIsIdentity(t *testing.T) {
	p, err := Parse(url.Values{})
	if err != nil {
		t.Fatalf("Parse empty: %v", err)
	}
	if p.W != nil || p.H != nil || p.Crop != "" || p.Fit != "" || p.Q != 0 {
		t.Fatalf("expected zero Params, got %+v", p)
	}
}

func TestParse_ValidAll(t *testing.T) {
	q := url.Values{
		"w":    {"800"},
		"h":    {"600"},
		"crop": {"top"},
		"fit":  {"contain"},
		"q":    {"90"},
	}
	p, err := Parse(q)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.W == nil || *p.W != 800 {
		t.Errorf("W: got %v", p.W)
	}
	if p.H == nil || *p.H != 600 {
		t.Errorf("H: got %v", p.H)
	}
	if p.Crop != "top" {
		t.Errorf("Crop: got %q", p.Crop)
	}
	if p.Fit != "contain" {
		t.Errorf("Fit: got %q", p.Fit)
	}
	if p.Q != 90 {
		t.Errorf("Q: got %d", p.Q)
	}
}

func TestParse_Validation(t *testing.T) {
	cases := []struct {
		name string
		q    url.Values
	}{
		{"w_zero", url.Values{"w": {"0"}}},
		{"w_negative", url.Values{"w": {"-1"}}},
		{"w_not_int", url.Values{"w": {"abc"}}},
		{"h_zero", url.Values{"h": {"0"}}},
		{"h_negative", url.Values{"h": {"-5"}}},
		{"h_not_int", url.Values{"h": {"1.5"}}},
		{"crop_invalid", url.Values{"crop": {"middle"}}},
		{"fit_invalid", url.Values{"fit": {"stretch"}}},
		{"q_zero", url.Values{"q": {"0"}}},
		{"q_over", url.Values{"q": {"101"}}},
		{"q_not_int", url.Values{"q": {"high"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(tc.q); err == nil {
				t.Fatalf("expected error for %v", tc.q)
			}
		})
	}
}

func TestParse_ValidCropAndFitValues(t *testing.T) {
	crops := []string{"center", "top", "bottom", "left", "right"}
	fits := []string{"cover", "contain", "fill"}
	for _, crop := range crops {
		if _, err := Parse(url.Values{"crop": {crop}}); err != nil {
			t.Errorf("crop %q: %v", crop, err)
		}
	}
	for _, fit := range fits {
		if _, err := Parse(url.Values{"fit": {fit}}); err != nil {
			t.Errorf("fit %q: %v", fit, err)
		}
	}
}

func TestNormalize_Defaults(t *testing.T) {
	t.Run("identity_q_default", func(t *testing.T) {
		n := Normalize(Params{})
		if n.Q != 80 {
			t.Errorf("Q: want 80, got %d", n.Q)
		}
		if n.Crop != "" || n.Fit != "" {
			t.Errorf("identity should not set crop/fit, got crop=%q fit=%q", n.Crop, n.Fit)
		}
		if n.W != nil || n.H != nil {
			t.Errorf("identity should keep nil dimensions")
		}
	})

	t.Run("q_zero_becomes_80", func(t *testing.T) {
		n := Normalize(Params{Q: 0})
		if n.Q != 80 {
			t.Errorf("Q: want 80, got %d", n.Q)
		}
	})

	t.Run("q_preserved", func(t *testing.T) {
		n := Normalize(Params{Q: 55})
		if n.Q != 55 {
			t.Errorf("Q: want 55, got %d", n.Q)
		}
	})

	t.Run("resize_defaults_crop_fit", func(t *testing.T) {
		w := 100
		n := Normalize(Params{W: &w})
		if n.Crop != "center" {
			t.Errorf("Crop: want center, got %q", n.Crop)
		}
		if n.Fit != "cover" {
			t.Errorf("Fit: want cover, got %q", n.Fit)
		}
		if n.Q != 80 {
			t.Errorf("Q: want 80, got %d", n.Q)
		}
	})

	t.Run("resize_preserves_explicit", func(t *testing.T) {
		h := 200
		n := Normalize(Params{H: &h, Crop: "left", Fit: "fill", Q: 70})
		if n.Crop != "left" || n.Fit != "fill" || n.Q != 70 {
			t.Errorf("got %+v", n)
		}
	})
}

func TestHash_StabilityAndOrder(t *testing.T) {
	w, h := 800, 600
	a := Params{W: &w, H: &h, Crop: "center", Fit: "cover", Q: 80}
	b := Params{H: &h, W: &w, Fit: "cover", Crop: "center", Q: 80}

	ha, hb := Hash(a), Hash(b)
	if ha != hb {
		t.Fatalf("hash must be order-independent: %s vs %s", ha, hb)
	}
	if len(ha) != 64 {
		t.Fatalf("want 64 hex chars, got %d (%q)", len(ha), ha)
	}

	// Same as hashing the canonical ordered key of normalized params.
	wantSum := sha256.Sum256([]byte(canonicalKey(Normalize(a))))
	want := hex.EncodeToString(wantSum[:])
	if ha != want {
		t.Fatalf("Hash mismatch: got %s want %s", ha, want)
	}

	// Different params → different hash.
	c := Params{W: &w, H: &h, Crop: "top", Fit: "cover", Q: 80}
	if Hash(c) == ha {
		t.Fatal("different crop must change hash")
	}
}

func TestHash_NormalizeBeforeHash(t *testing.T) {
	w := 100
	raw := Params{W: &w} // q=0, empty crop/fit
	norm := Normalize(raw)
	if Hash(raw) != Hash(norm) {
		t.Fatal("Hash must normalize before digesting")
	}
	if Hash(Params{}) != Hash(Params{Q: 80}) {
		t.Fatal("identity with/without explicit default q must match")
	}
}

func TestCacheKeyJSON(t *testing.T) {
	w, h := 320, 240
	raw := Params{W: &w, H: &h}
	b := CacheKeyJSON(raw)

	var got Params
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.W == nil || *got.W != 320 || got.H == nil || *got.H != 240 {
		t.Errorf("dimensions: %+v", got)
	}
	if got.Q != 80 || got.Crop != "center" || got.Fit != "cover" {
		t.Errorf("defaults missing in JSON: %+v", got)
	}

	identity := CacheKeyJSON(Params{})
	var id Params
	if err := json.Unmarshal(identity, &id); err != nil {
		t.Fatalf("identity unmarshal: %v", err)
	}
	if id.Q != 80 || id.W != nil || id.H != nil || id.Crop != "" || id.Fit != "" {
		t.Errorf("identity JSON: %+v", id)
	}
}
