package imgproxy

import (
	"fmt"
	"path"
	"strings"

	"github.com/notrealscooby/image-optimizer/internal/transform"
)

// BuildPath builds an unsigned imgproxy request path for an original under
// IMGPROXY_LOCAL_FILESYSTEM_ROOT (e.g. originals/{id}.jpg → local:///originals/...).
//
// Format: /insecure/rs:{fit}:{w}:{h}/g:{crop}/q:{q}/plain/local:///{path}@avif
func BuildPath(originalPath string, p transform.Params) string {
	w, h := 0, 0
	if p.W != nil {
		w = *p.W
	}
	if p.H != nil {
		h = *p.H
	}

	segments := []string{
		"insecure",
		fmt.Sprintf("rs:%s:%d:%d", resizingType(p.Fit), w, h),
		fmt.Sprintf("g:%s", gravity(p.Crop)),
		fmt.Sprintf("q:%d", p.Q),
		"plain",
		fmt.Sprintf("local:///%s@avif", localPath(originalPath)),
	}
	return "/" + strings.Join(segments, "/")
}

func localPath(originalPath string) string {
	p := path.Clean("/" + strings.TrimSpace(originalPath))
	return strings.TrimPrefix(p, "/")
}

// resizingType maps API fit → imgproxy resizing_type.
// cover→fill, contain→fit, fill→force; empty defaults to fit.
func resizingType(fit string) string {
	switch strings.ToLower(strings.TrimSpace(fit)) {
	case "cover":
		return "fill"
	case "contain", "":
		return "fit"
	case "fill":
		return "force"
	default:
		return "fit"
	}
}

// gravity maps API crop → imgproxy gravity type.
// center→ce, top→no, bottom→so, left→we, right→ea; empty defaults to ce.
func gravity(crop string) string {
	switch strings.ToLower(strings.TrimSpace(crop)) {
	case "center", "":
		return "ce"
	case "top":
		return "no"
	case "bottom":
		return "so"
	case "left":
		return "we"
	case "right":
		return "ea"
	default:
		return "ce"
	}
}
