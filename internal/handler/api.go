// Package handler serves one endpoint at four versions.
package handler

import (
	"fmt"
	"time"

	"github.com/usmanvirk/api-versioning/internal/apiver"
	"github.com/usmanvirk/api-versioning/internal/migrate"
)

// Listing is the current shape, and the only one the handler builds.
//
// v4. Everything below it is produced by the chain.
type Listing struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Grade    string   `json:"grade"`
	Price    Money    `json:"price"`
	Original *Money   `json:"original_price,omitempty"`
	Images   []Image  `json:"images"`
	Stock    Stock    `json:"stock"`
	Seller   Seller   `json:"seller"`
	Warranty Warranty `json:"warranty"`
	Battery  int      `json:"battery_health_pct,omitempty"`
}

type Money struct {
	Cents    int64  `json:"cents"`
	Currency string `json:"currency"`
}

type Image struct {
	URL   string `json:"url"`
	Width int    `json:"width"`
}

type Stock struct {
	Available bool `json:"available"`
	Count     int  `json:"count"`
}

type Seller struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Rating float64 `json:"rating"`
}

type Warranty struct {
	Months   int  `json:"months"`
	Extended bool `json:"extended"`
}

// Versions is the registry this service runs with.
//
// v1 is deprecated with a date on it, which is the only lever a server has
// over an app it cannot update: say so on every response, early, and keep
// saying it.
func Versions(now time.Time) (*apiver.Registry, error) {
	return apiver.NewRegistry(
		apiver.Spec{
			Version: 1, Status: apiver.Deprecated,
			SunsetOn: now.AddDate(0, 3, 0),
			Note:     "price was a bare integer in minor units with no currency",
		},
		apiver.Spec{Version: 2, Status: apiver.Supported,
			Note: "stock was two top level booleans rather than an object"},
		apiver.Spec{Version: 3, Status: apiver.Supported,
			Note: "images were a list of strings"},
		apiver.Spec{Version: 4, Status: apiver.Current,
			Note: "current"},
	)
}

// Chain is the set of down-migrations.
//
// Read bottom to top to see the history of the endpoint: each step undoes
// one change, and every one of them was written once and has not been
// touched since.
func Chain() (*migrate.Chain, error) {
	return migrate.NewChain(
		// v4 to v3. v4 added the battery reading and made warranty an
		// object; v3 had a bare integer and no battery.
		migrate.Step{
			From: 4,
			Why:  "warranty became an object and a battery reading was added",
			Down: func(d migrate.Doc) error {
				if w, ok := d["warranty"].(map[string]any); ok {
					d["warranty_months"] = w["months"]
				}
				migrate.Drop(d, "warranty", "battery_health_pct")
				return nil
			},
		},

		// v3 to v2. v3 gave each image a width; v2 was a list of URLs.
		migrate.Step{
			From: 3,
			Why:  "images gained a width, so they became objects rather than strings",
			Down: func(d migrate.Doc) error {
				raw, ok := d["images"]
				if !ok {
					return nil
				}
				list, ok := raw.([]any)
				if !ok {
					return fmt.Errorf("images is %T, not a list", raw)
				}
				urls := make([]any, 0, len(list))
				for i, item := range list {
					m, ok := item.(map[string]any)
					if !ok {
						return fmt.Errorf("images[%d] is %T", i, item)
					}
					urls = append(urls, m["url"])
				}
				d["images"] = urls
				return nil
			},
		},

		// v2 to v1. v2 made stock an object and money an object. v1 had a
		// bare integer price with the currency implied, which is the change
		// that forced a new version in the first place: the moment the
		// marketplace sold outside the euro zone, an integer with an implied
		// currency was wrong and no client could tell.
		migrate.Step{
			From: 2,
			Why:  "price became an object with a currency, and stock became an object",
			Down: func(d migrate.Doc) error {
				if p, ok := d["price"].(map[string]any); ok {
					d["price"] = p["cents"]
				}
				if o, ok := d["original_price"].(map[string]any); ok {
					d["original_price"] = o["cents"]
				}
				if s, ok := d["stock"].(map[string]any); ok {
					d["in_stock"] = s["available"]
					d["stock_count"] = s["count"]
				}
				migrate.Drop(d, "stock")
				return nil
			},
		},
	)
}
