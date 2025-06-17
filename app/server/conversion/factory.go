package conversion

import (
	"github.com/ninaiad/fq-connector-go/app/config"
)

func NewCollection(cfg *config.TConversionConfig) Collection {
	if cfg.UseUnsafeConverters {
		return collectionUnsafe{}
	}

	return collectionDefault{}
}
