package main

import (
	"fmt"
	"math"
	"sync/atomic"
	"time"
)

func clientStat() {

	var cacheHitRatio float64
	var startTime time.Time
	var totalResponseTimeStat time.Duration
	var totalFirstByteTimeStat time.Duration
	var avgFirstByteTimeStat, avgResponseTimeStat time.Duration
	var minFirstByteTimeStat, maxFirstByteTimeStat time.Duration
	var minResponseTimeStat, maxResponseTimeStat time.Duration
	var round int64

	startTime = time.Now()

	// 监控协程
	go func() {
		ticker := time.NewTicker(config.tickerDump)
		defer ticker.Stop()
		var cnt, cacheHits int64

		for {
			select {
			case <-done:
				return
			case reqStat := <-reqStatCh:
				// 处理请求统计信息（如果需要）
				cnt++
				totalFirstByteTimeStat += reqStat.firstByteTime
				totalResponseTimeStat += reqStat.respTime
				avgFirstByteTimeStat = time.Duration(int64(totalFirstByteTimeStat) / cnt)
				avgResponseTimeStat = time.Duration(int64(totalResponseTimeStat) / cnt)
				if minFirstByteTimeStat == 0 {
					minFirstByteTimeStat = time.Duration(reqStat.firstByteTime)
				} else {
					minFirstByteTimeStat = time.Duration(math.Min(float64(minFirstByteTimeStat), float64(reqStat.firstByteTime)))
				}

				if minResponseTimeStat == 0 {
					minResponseTimeStat = time.Duration(reqStat.respTime)
				} else {
					minResponseTimeStat = time.Duration(math.Min(float64(minResponseTimeStat), float64(reqStat.respTime)))
				}

				if maxFirstByteTimeStat == 0 {
					maxFirstByteTimeStat = time.Duration(reqStat.firstByteTime)
				} else {
					maxFirstByteTimeStat = time.Duration(math.Max(float64(maxFirstByteTimeStat), float64(reqStat.firstByteTime)))
				}

				if maxResponseTimeStat == 0 {
					maxResponseTimeStat = time.Duration(reqStat.respTime)
				} else {
					maxResponseTimeStat = time.Duration(math.Max(float64(maxResponseTimeStat), float64(reqStat.respTime)))
				}

				if reqStat.cacheHit {
					cacheHits++
					cacheHitRatio = float64(cacheHits) / float64(cnt) * 100
				}

			case <-ticker.C:
				round++
				elapsed := time.Since(startTime).Seconds()
				currentTotal := atomic.LoadInt64(&totalRequests)

				// 计算平均时间指标
				fmt.Printf("统计次%d: 总请求数=%d, 成功=%d, 失败=%d, 总字节数=%d, QPS=%.2f, 已用时=%.2fs, 缓存命中率=%.2f%%\n\n",
					round, currentTotal, successRequests, failedRequests, totalBytes,
					float64(currentTotal)/elapsed, elapsed, cacheHitRatio)
				fmt.Printf("      》》》平均首包时间=%v, 平均响应时间=%v, 最大首包时间=%v, 最大响应时间=%v 最小首包时间=%v, 最小响应时间=%v\n\n\n\n",
					avgFirstByteTimeStat, avgResponseTimeStat, maxFirstByteTimeStat, maxResponseTimeStat, minFirstByteTimeStat, minResponseTimeStat)

				totalFirstByteTimeStat = 0
				totalResponseTimeStat = 0
				cnt = 0
				minFirstByteTimeStat = 0
				maxFirstByteTimeStat = 0
				minResponseTimeStat = 0
				maxResponseTimeStat = 0
				cacheHits = 0
				cacheHitRatio = 0
			}
		}

	}()

}
