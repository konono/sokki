package translation

import (
	"context"
	"fmt"
)

// Provider is private to the translation worker; both adapters have the same
// bounded request lifecycle, while only Ollama performs an actual warm load.
type Provider interface {
	preload(context.Context) error
	translate(context.Context, []string, []string, func(string)) (string, error)
	close()
}

func makeProvider(name, model, url string) (Provider, error) {
	switch name {
	case "ollama":
		return newClient(model, url)
	case "claude":
		return newClaude(model)
	default:
		return nil, fmt.Errorf("翻訳 provider %q は未対応です", name)
	}
}
