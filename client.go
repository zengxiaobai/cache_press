package main

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"cache_press/pkg/buffer"

	"go.uber.org/ratelimit"
)

// 结束信号
var done = make(chan bool)
var totalRequests, successRequests, failedRequests int64
var totalBytes int64

func getBaseURL() string {
	if config.addr != "" {
		if !strings.HasPrefix(config.addr, "http") {
			return fmt.Sprintf("http://%s", config.addr)
		}
		return config.addr
	}
	return fmt.Sprintf("http://%s:%d", config.host, config.port)
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

// 使用unsafe将字节切片转换为字符串，避免内存分配和复制
func bytesToString(b []byte) string {
	return *(*string)(unsafe.Pointer(&b))
}

// 使用unsafe将字符串转换为字节切片，避免内存分配和复制
// 注意：返回的切片不应被修改，因为字符串在Go中是不可变的
func stringToBytes(s string) []byte {
	return *(*[]byte)(unsafe.Pointer(
		&struct {
			string
			Cap int
		}{s, len(s)},
	))
}

// 添加随机字符串生成函数
func randString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	bPtr := buffer.GetBytes(1024)
	defer buffer.PutBytes(bPtr)
	b := *bPtr

	// 如果需要的长度超过池中切片的长度，创建一个新的
	if n > cap(b) {
		b = make([]byte, n)
	} else {
		b = b[:n]
	}

	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}

	// 使用unsafe避免内存分配和复制
	return bytesToString(b)
}

func runClient() {
	// 构建目标URL基础
	var baseURL = getBaseURL()

	limiter := ratelimit.New(config.qps)

	var wg sync.WaitGroup

	// 控制并发连接数
	semaphore := make(chan struct{}, config.conns)

	// 统计变量

	var cacheHits int64
	startTime := time.Now()

	clientStat()
	// 创建多个goroutine模拟并发请求
	for i := 0; i < config.conns; i++ {
		wg.Add(1)
		go func(connID int) {
			defer wg.Done()

			client := &http.Client{
				Timeout:   30 * time.Second,
				Transport: transport,
			}

			for {
				// 检查是否到达结束时间
				if time.Since(startTime) >= config.duration {
					break
				}

				// 限制QPS
				limiter.Take()

				// 获取信号量控制并发数
				semaphore <- struct{}{}

				// 生成随机URL
				url := generateRandomURL(baseURL, config.urlCount, config.hitRatio)

				// 创建请求
				req, err := http.NewRequest("GET", url, nil)
				if err != nil {
					<-semaphore
					continue
				}

				// 设置请求头 - 包括x-press-size头
				req.Header.Set("x-press-size", strconv.Itoa(getRespSize()))
				req.Header.Set("User-Agent", fmt.Sprintf("PressureTestClient-%d", connID))
				req.Header.Set(config.ReqIDHdrName, fmt.Sprintf("PressureTestClient-%d-%d-%s", connID, time.Now().UnixNano(), randString(6)))

				// 根据CloseConn参数决定是否关闭连接
				if config.CloseConn > 0 && rand.Float64() <= config.CloseConn {
					req.Header.Set("Connection", "close")
				} else {
					req.Header.Set("Connection", "keep-alive")
				}

				req.URL.Host = config.addr
				req.Host = config.host
				// 记录请求开始时间
				requestStartTime := time.Now()
				errFunc := func(err error) {
					fmt.Println(req.URL.RequestURI(), err)
					atomic.AddInt64(&failedRequests, 1)
					atomic.AddInt64(&totalRequests, 1)
					<-semaphore
					if !config.ignoreErr {
						os.Exit(1)
					}
				}
				// 发送请求
				resp, err := client.Do(req)
				if err != nil {
					// 记录失败请求
					errFunc(err)
					continue
				}
				defer resp.Body.Close()
				// 记录首包时间（收到响应头的时间）
				firstByteTime := time.Since(requestStartTime)
				// 根据clientSendCloseProb决定是否在发送完请求后主动断开连接
				if config.clientSendCloseProb > 0 && rand.Float64() <= config.clientSendCloseProb {
					if tcpConn, ok := resp.Body.(interface{ Close() error }); ok {
						tcpConn.Close()
					}
					continue
				}

				if resp.StatusCode > 300 {
					errFunc(fmt.Errorf("请求失败: %s", resp.StatusCode))
					continue
				}

				//fmt.Println(resp.Status, resp.Header)
				// 检查 X-Cache 头判断是否命中缓存
				cacheHit := false
				xCacheHeader := resp.Header.Get("X-Cache")
				if strings.Contains(xCacheHeader, "HIT") {
					cacheHit = true
				}

				// 读取响应体（分块读取，支持中途断开）
				var readBytes int64
				var totalExpected int64

				// 尝试获取Content-Length
				if clStr := resp.Header.Get("Content-Length"); clStr != "" {
					totalExpected, _ = strconv.ParseInt(clStr, 10, 64)
				}

				// 获取服务器返回的MD5值（如果有）
				serverMD5 := resp.Header.Get("X-Content-MD5")

				// 定义一个读取器，用于分块读取
				reader := resp.Body
				err = nil

				// 创建MD5哈希器（仅当服务器返回了MD5值时才计算）
				var hasher hash.Hash
				if serverMD5 != "" {
					hasher = md5.New()
				}

				// 分块读取响应体
				const chunkSize = 35840
				chunkPtr := buffer.GetBytes(35840)
				defer buffer.PutBytes(chunkPtr)
				chunk := *chunkPtr

				for {
					n, readErr := reader.Read(chunk)
					if n > 0 {
						readBytes += int64(n)

						// 如果需要计算MD5，更新哈希
						if serverMD5 != "" {
							hasher.Write(chunk[:n])
						}
					}

					// 检查是否需要在接收一半时断开连接
					if config.clientRecvHalfCloseProb > 0 && rand.Float64() <= config.clientRecvHalfCloseProb {
						if totalExpected > 0 {
							// 如果知道总大小，检查是否读取了一半
							if readBytes >= totalExpected/2 {
								break
							}
						} else {
							// 如果不知道总大小，随机在某个时刻断开
							if rand.Float64() <= 0.1 { // 10%概率在每次读取后断开
								break
							}
						}
					}

					if readErr != nil {
						if readErr != io.EOF {
							err = readErr
						}
						break
					}
				}

				// 如果服务器返回了MD5值，验证MD5是否匹配
				if serverMD5 != "" {
					calculatedMD5 := hex.EncodeToString(hasher.Sum(nil))

					// 如果启用了测试MD5失败模式，故意修改计算出的MD5值
					if config.testMD5Failure {
						// 修改MD5值的最后一个字符
						if len(calculatedMD5) > 0 {
							bytes := []byte(calculatedMD5)
							if bytes[len(bytes)-1] == '9' {
								bytes[len(bytes)-1] = '0'
							} else {
								bytes[len(bytes)-1] = '9'
							}
							calculatedMD5 = string(bytes)
						}
					}

					if calculatedMD5 != serverMD5 {
						fmt.Printf("MD5校验失败! 服务器MD5: %s, 客户端计算MD5: %s, URL: %s\n",
							serverMD5, calculatedMD5, req.URL.Path)
						if !config.ignoreErr {
							os.Exit(1)
						}
					}
				}

				// 记录完整响应时间（收到完整响应体的时间）
				responseTime := time.Since(requestStartTime)

				select {
				case reqStatCh <- reqStatInfo{
					firstByteTime: firstByteTime,
					respTime:      responseTime,
					cacheHit:      cacheHit,
				}:
				default:
					// Channel 满时丢弃数据，防止阻塞
					fmt.Println("！！！丢弃数据，统计通道已满！！！")
				}

				if err != nil {
					// 记录失败请求
					fmt.Println("read body err :",
						err, req.URL.Path, readBytes, time.Now().Format("2006-01-02 15:04:05.000"), req.Header.Get(config.ReqIDHdrName))
					if !config.ignoreErr {
						os.Exit(1)
					}
					failedRequests++
				} else {
					// 记录成功请求
					atomic.AddInt64(&successRequests, 1)
					totalBytes += readBytes
				}

				// 接收完响应后主动断开连接
				if config.clientRecvFullCloseProb > 0 && rand.Float64() <= config.clientRecvFullCloseProb {
					if resp.Body != nil {
						resp.Body.Close()
					}
				}

				atomic.AddInt64(&totalRequests, 1)
				<-semaphore
			}
		}(i)
	}

	// 等待所有协程完成
	wg.Wait()

	// 停止监控
	done <- true

	// 输出最终统计
	elapsed := time.Since(startTime).Seconds()
	finalTotal := atomic.LoadInt64(&totalRequests)
	finalHits := atomic.LoadInt64(&cacheHits)

	hitRate := 0.0
	if finalTotal > 0 {
		hitRate = float64(finalHits) / float64(finalTotal) * 100
	}

	fmt.Printf("\n=== 最终统计 ===\n")
	fmt.Printf("目标地址: %s\n", baseURL)
	fmt.Printf("响应大小范围: %v, 小响应体比例: %.2f\n", config.respSizeRange, config.diskRatio)
	fmt.Printf("总请求数: %d\n", totalRequests)
	fmt.Printf("成功请求数: %d\n", successRequests)
	fmt.Printf("失败请求数: %d\n", failedRequests)
	fmt.Printf("缓存命中数: %d\n", finalHits)
	fmt.Printf("缓存命中率: %.2f%%\n", hitRate)
	fmt.Printf("总传输字节数: %d\n", totalBytes)
	fmt.Printf("平均QPS: %.2f\n", float64(totalRequests)/elapsed)
	fmt.Printf("成功率: %.2f%%\n", float64(successRequests)/float64(totalRequests)*100)
	fmt.Printf("总耗时: %.2fs\n", elapsed)
}
