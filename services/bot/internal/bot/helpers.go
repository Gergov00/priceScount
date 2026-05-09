package bot

import (
	"fmt"
	"strconv"
	"strings"
)

func parsePrice(text string) (float64, error) {
	text = strings.ReplaceAll(text, " ", "")
	text = strings.ReplaceAll(text, ",", ".")
	price, err := strconv.ParseFloat(text, 64)
	if err != nil || price <= 0 {
		return 0, fmt.Errorf("invalid price")
	}
	return price, nil
}

func parseIndex(data, prefix string) (int, error) {
	return strconv.Atoi(strings.TrimPrefix(data, prefix))
}
