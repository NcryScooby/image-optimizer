package imgproxy

import (
	"strings"
	"testing"

	"github.com/notrealscooby/image-optimizer/internal/transform"
)

func intPtr(n int) *int { return &n }

const testBucket = "images"

func TestBuildPath_FullParams(t *testing.T) {
	p := transform.Params{
		W:    intPtr(300),
		H:    intPtr(200),
		Crop: "center",
		Fit:  "cover",
		Q:    80,
	}
	got := BuildPath(testBucket, "originals/abc.jpg", p)

	want := "/insecure/rs:fill:300:200/g:ce/q:80/plain/s3://images/originals/abc.jpg@avif"
	if got != want {
		t.Fatalf("BuildPath() = %q, want %q", got, want)
	}
}

func TestBuildPath_FitMapping(t *testing.T) {
	tests := []struct {
		fit  string
		want string
	}{
		{"cover", "rs:fill:"},
		{"contain", "rs:fit:"},
		{"fill", "rs:force:"},
		{"", "rs:fit:"},
	}
	for _, tt := range tests {
		t.Run(tt.fit, func(t *testing.T) {
			got := BuildPath(testBucket, "originals/x.png", transform.Params{
				W: intPtr(100), H: intPtr(50), Fit: tt.fit, Q: 90,
			})
			if !strings.Contains(got, tt.want) {
				t.Fatalf("fit=%q path %q missing %q", tt.fit, got, tt.want)
			}
		})
	}
}

func TestBuildPath_CropMapping(t *testing.T) {
	tests := []struct {
		crop string
		want string
	}{
		{"center", "g:ce"},
		{"top", "g:no"},
		{"bottom", "g:so"},
		{"left", "g:we"},
		{"right", "g:ea"},
		{"", "g:ce"},
	}
	for _, tt := range tests {
		t.Run(tt.crop, func(t *testing.T) {
			got := BuildPath(testBucket, "originals/x.webp", transform.Params{
				W: intPtr(10), H: intPtr(10), Crop: tt.crop, Fit: "contain", Q: 70,
			})
			if !strings.Contains(got, "/"+tt.want+"/") {
				t.Fatalf("crop=%q path %q missing /%s/", tt.crop, got, tt.want)
			}
		})
	}
}

func TestBuildPath_NilDimensions(t *testing.T) {
	got := BuildPath(testBucket, "originals/id.jpeg", transform.Params{Q: 80})
	want := "/insecure/rs:fit:0:0/g:ce/q:80/plain/s3://images/originals/id.jpeg@avif"
	if got != want {
		t.Fatalf("BuildPath() = %q, want %q", got, want)
	}
}

func TestBuildPath_StripsLeadingSlash(t *testing.T) {
	got := BuildPath(testBucket, "/originals/abc.jpg", transform.Params{Q: 50, Fit: "contain"})
	if !strings.Contains(got, "s3://images/originals/abc.jpg@avif") {
		t.Fatalf("unexpected s3 source in %q", got)
	}
	if strings.Contains(got, "s3://images//") {
		t.Fatalf("double slash in s3 source: %q", got)
	}
}

func TestBuildPath_AlwaysAVIF(t *testing.T) {
	got := BuildPath(testBucket, "originals/a.png", transform.Params{Q: 1})
	if !strings.HasSuffix(got, "@avif") {
		t.Fatalf("expected @avif suffix, got %q", got)
	}
	if !strings.Contains(got, "/plain/") {
		t.Fatalf("expected /plain/ segment, got %q", got)
	}
}
