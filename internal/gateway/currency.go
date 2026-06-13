package gateway

import (
	"strings"

	"github.com/linlay/transit-hub/internal/store"
)

func (g *Gateway) configuredCurrency() string {
	currency := strings.ToUpper(strings.TrimSpace(g.env.Currency))
	if currency == "" {
		currency = strings.ToUpper(strings.TrimSpace(store.DefaultCurrency))
	}
	if currency == "" {
		currency = "CNY"
	}
	return currency
}
