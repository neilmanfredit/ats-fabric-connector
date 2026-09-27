package bullhorn

import "github.com/bruin-data/ingestr/internal/registry"

func init() {
	registry.RegisterSource(
		[]string{"bullhorn"},
		func() any { return NewBullhornSource() },
	)
}
