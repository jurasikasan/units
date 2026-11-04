package http

import (
	"fmt"
)

const trimLen = 100

func buildPrefixPostfixTrimmedStr(bodyBytes []byte) string {
	totalLen := len(bodyBytes)
	if totalLen <= trimLen*2 {
		return string(bodyBytes)
	}
	return fmt.Sprintf(
		"%s ..... %s",
		string(bodyBytes[:trimLen]), string(bodyBytes[len(bodyBytes)-trimLen:]),
	)
}
