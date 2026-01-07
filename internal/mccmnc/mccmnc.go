package mccmnc

import (
	"encoding/json"
	"os"
	"sync"
)

// NetworkOperator represents an entry in mcc_mnc.json
type NetworkOperator struct {
	MCC         string `json:"mcc"`
	MNC         string `json:"mnc"`
	ISO         string `json:"iso"`
	Country     string `json:"country"`
	CountryCode string `json:"country_code"`
	Name        string `json:"name"`
}

var (
	operators []NetworkOperator
	once      sync.Once
)

// LoadOperators loads the mcc_mnc.json file
func LoadOperators(path string) error {
	var err error
	once.Do(func() {
		file, e := os.ReadFile(path)
		if e != nil {
			err = e
			return
		}
		if e := json.Unmarshal(file, &operators); e != nil {
			err = e
			return
		}
	})
	return err
}

// GetOperatorName finds the operator name for a given MCC and MNC
func GetOperatorName(mcc, mnc string) string {
	for _, op := range operators {
		if op.MCC == mcc && op.MNC == mnc {
			return op.Name
		}
	}
	return ""
}
