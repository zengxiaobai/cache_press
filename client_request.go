package main

import (
	"fmt"
	"io"
	"math/rand"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"time"
)

type RangeSpec struct {
	Start int64
	End   int64
}

func parseRangeSpec(rangeStr string) []RangeSpec {
	if rangeStr == "" {
		return nil
	}

	rangeStr = strings.TrimSpace(rangeStr)
	if !strings.HasPrefix(rangeStr, "[") || !strings.HasSuffix(rangeStr, "]") {
		return nil
	}

	inner := strings.TrimPrefix(strings.TrimSuffix(rangeStr, "]"), "[")
	parts := strings.Split(inner, ",")

	var ranges []RangeSpec
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		rangeParts := strings.Split(part, "-")
		if len(rangeParts) != 2 {
			continue
		}

		start, err1 := strconv.ParseInt(strings.TrimSpace(rangeParts[0]), 10, 64)
		end, err2 := strconv.ParseInt(strings.TrimSpace(rangeParts[1]), 10, 64)

		if err1 != nil || err2 != nil {
			continue
		}

		ranges = append(ranges, RangeSpec{Start: start, End: end})
	}

	return ranges
}

func generateRandomRange(spec RangeSpec) (int64, int64) {
	if spec.Start >= spec.End {
		return spec.Start, spec.End
	}

	start := spec.Start + rand.Int63n(spec.End-spec.Start)
	end := start + rand.Int63n(spec.End-start+1)

	return start, end
}

func getRespSize() int {
	if len(config.respSizeRange) == 1 {
		return config.respSizeRange[0]
	}
	minSize, maxSize := config.respSizeRange[0], config.respSizeRange[1]
	if rand.Float64() <= config.diskRatio {
		return minSize
	}
	return maxSize
}

func createRequest(connID int, baseURL string) (*http.Request, error) {
	url := generateRandomURL(baseURL, config.urlCount, config.hitRatio)

	respSize := getRespSize()

	parsedURL, err := neturl.Parse(url)
	if err == nil {
		if pressSize := parsedURL.Query().Get("x-press-size"); pressSize != "" {
			if size, err := strconv.Atoi(pressSize); err == nil {
				respSize = size
			}
		}
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("x-press-size", strconv.Itoa(respSize))
	req.Header.Set("User-Agent", fmt.Sprintf("PressureTestClient-%d", connID))
	req.Header.Set(config.ReqIDHdrName, fmt.Sprintf("PressureTestClient-%d-%d-%s", connID, time.Now().UnixNano(), randString(6)))

	if config.CloseConn > 0 && rand.Float64() <= config.CloseConn {
		req.Header.Set("Connection", "close")
	} else {
		req.Header.Set("Connection", "keep-alive")
	}

	// Range 请求：如果配置了随机无 Range 概率，则随机决定是否带 Range 头
	if config.enableRange {
		ranges := parseRangeSpec(config.rangeStr)
		shouldAddRange := true
		if config.randomNoRangeProb > 0 {
			// 根据配置的概率决定是否带 Range 头
			shouldAddRange = rand.Float64() >= config.randomNoRangeProb
		}
		if len(ranges) > 0 && shouldAddRange {
			var rangeParts []string
			for _, spec := range ranges {
				var start, end int64
				if config.rangeRandom {
					start, end = generateRandomRange(spec)
				} else {
					start, end = spec.Start, spec.End
				}
				rangeParts = append(rangeParts, fmt.Sprintf("%d-%d", start, end))
			}
			req.Header.Set("Range", fmt.Sprintf("bytes=%s", strings.Join(rangeParts, ",")))
		}
	}

	req.URL.Host = config.addr
	req.Host = config.host

	// 添加自定义请求头
	for i := 0; i < len(config.customHeaders); i += 2 {
		if i+1 < len(config.customHeaders) {
			req.Header.Set(config.customHeaders[i], config.customHeaders[i+1])
		}
	}

	return req, nil
}

// sendPurgeRequest 发送 PURGE 请求
// PURGE 请求不校验 MD5：仅发送 purge-type 头并打印响应状态，
// 不读取/计算响应体 MD5，仅 drain body 以便连接复用。
func sendPurgeRequest(client *http.Client, purgeURL string) {
	purgeReq, err := http.NewRequest("PURGE", purgeURL, nil)
	if err != nil {
		fmt.Printf("创建 PURGE 请求失败: %v\n", err)
		return
	}
	purgeReq.Header.Set("purge-type", "file,hard")
	purgeReq.Header.Set("User-Agent", "PressureTestClient-Purge")
	purgeReq.URL.Host = config.addr
	purgeReq.Host = config.host

	resp, err := client.Do(purgeReq)
	if err != nil {
		fmt.Printf("PURGE 请求失败 %s: %v\n", purgeURL, err)
		return
	}
	// drain body 以便连接复用，但不做任何 MD5 校验
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	fmt.Printf("PURGE %s: %s\n", purgeURL, resp.Status)
}
