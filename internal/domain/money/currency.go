package money

import (
	"errors"
	"fmt"
)

var ErrInvalidCurrency = errors.New("invalid currency")

var supportedCurrencies = map[string]struct{}{
	"BRL": {},
	"EUR": {},
	"USD": {},
}

type Currency struct {
	code string
}

func ParseCurrency(code string) (Currency, error) {
	if _, ok := supportedCurrencies[code]; !ok {
		return Currency{}, fmt.Errorf("%w: %q is not supported", ErrInvalidCurrency, code)
	}
	return Currency{code: code}, nil
}

func (c Currency) Code() string {
	return c.code
}

func (c Currency) Valid() bool {
	_, ok := supportedCurrencies[c.code]
	return ok
}
