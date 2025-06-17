package datasource

import (
	api_common "github.com/ninaiad/fq-connector-go/api/common"
)

type DataSource struct {
	Instances []*api_common.TGenericDataSourceInstance
}
