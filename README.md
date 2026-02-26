# cache_press 压测工具

## 简介
cache_press 是一个用于测试缓存服务器性能的压测工具，支持客户端和服务器模式。

## 快速开始

### 服务端模式
```bash
./cache_press -mode=server -port=9000
```

### 客户端模式
```bash
./cache_press -mode=client -addr=192.168.233.43:8081 -conns=1000 -qps=3000 -duration=600s -hit-ratio=0.85 -url-count=1000000 -resp-size=1024 -disk-ratio=0.7 -host test.com -defer-start=3
```

## 命令行参数

### 通用参数

| 参数 | 描述 | 默认值 | 适用模式 |
|------|------|--------|----------|
| `-mode` | 运行模式: server/client | "server" | 所有 |
| `-host` | 服务器主机名或IP | "localhost" | 所有 |
| `-port` | 服务器端口 | 9000 | 所有 |
| `-addr` | 服务器完整地址 (格式: host:port)，如果设置了此参数则忽略host和port | "" | 所有 |
| `-log-dir` | 日志目录 | "" | 所有 |
| `-listen-ip` | 服务器监听IP | "" | 所有 |

### 客户端专用参数

| 参数 | 描述 | 默认值 |
|------|------|--------|
| `-conns` | 并发连接数 | 10 |
| `-qps` | QPS限制 | 100 |
| `-duration` | 压测持续时间 | 30s |
| `-ticker-dump` | 定时输出统计信息间隔 | 5s |
| `-resp-size` | 响应大小，格式: 单个数字或范围 [min,max] | "1024" |
| `-disk-ratio` | 小响应体比例 (0.0-1.0) | 0.5 |
| `-hit-ratio` | CDN命中率 (0.0-1.0) | 0.5 |
| `-url-count` | 总URL数量 | 1000000 |
| `-fixed-url` | 固定 URL 列表 (URI格式，不含host，多个用逗号分隔) | "" |
| `-max-requests` | 最大请求数量 (0表示不限制) | 0 |
| `-ignore-err` | 忽略错误 | false |
| `-defer-start` | 延迟启动时间(秒) | 0 |
| `-delay-resp-hdr` | 延迟响应头时间(毫秒) | 0 |
| `-delay-resp-hdr-random` | 延迟响应头随机时间(毫秒) | 0 |
| `-delay-resp-body` | 延迟响应体时间(毫秒) | 0 |
| `-delay-resp-body-random` | 延迟响应体随机时间(毫秒) | 0 |
| `-chunk-resp` | 分块响应比例 (0.0-1.0) | 0.0 |
| `-client-close-conn-prob` | 请求后关闭连接比例 (0.0-1.0) | 0.0 |
| `-req-id-hdr-name` | 请求ID头名称 | "X-WYCDN-Request-ID" |
| `-max-idle-conns` | 最大空闲连接数 | 2000 |
| `-max-idle-conns-per-host` | 每个主机最大空闲连接数 | 1000 |
| `-idle-conn-timeout` | 空闲连接超时时间 | 100s |
| `-client-send-close-prob` | 发送完请求后主动断开连接的概率 (0.0-1.0) | 0.0 |
| `-client-recv-half-close-prob` | 接收响应body一半时主动断开连接的概率 (0.0-1.0) | 0.0 |
| `-client-recv-full-close-prob` | 接收完响应后主动断开连接的概率 (0.0-1.0) | 0.0 |
| `-req-header-file` | 自定义请求头文件路径 (格式: 每行 header: value) | "" |
| `-test-hash-failure` | 测试哈希校验失败 | false |

### 客户端 Range 请求参数

| 参数 | 描述 | 默认值 |
|------|------|--------|
| `-range` | Range 配置字符串，格式: "[0-2048,2049-5000]" | "" |
| `-range-random` | 是否在每个 range 上下限之间随机 | false |

### 服务端专用参数

| 参数 | 描述 | 默认值 |
|------|------|--------|
| `-cache-resp` | 启用响应体缓存 | false |
| `-random-content` | 使用随机内容生成响应体 (默认 false 使用重复模式) | false |
| `-enable-hash` | 启用哈希校验 | false |
| `-multi-range-chunked` | multi range 使用 chunked 传输 (默认 false 使用 Content-Length) | false |
| `-pre-compress` | 预压缩整个文件后再支持 Range (类似 Nginx 的 gzip_static) | false |
| `-log-request-headers` | 是否打印请求头 | false |
| `-log-response-headers` | 是否打印响应头 | false |
| `-server-keep-alive-prob` | Connection头为keep-alive的概率 (0.0-1.0) | 1.0 |
| `-server-close-conn-after-body-prob` | 发完body后主动关闭连接的概率 (0.0-1.0) | 0.0 |
| `-send-bytes-per-interval` | 每次发送的字节数 (0表示不限制) | 0 |
| `-send-interval-ms` | 每次发送后的 sleep 时间 (毫秒) | 0 |
| `-resp-rate` | 响应速率限制 (格式: "10MB/s" 或 "100KB/s") | "" |
| `-resp-header-file` | 响应头文件路径 (格式: 每行一个头和值，头跟值中间用空格分开) | "" |
| `-use-chunked-transfer` | 是否使用 chunked 传输 (默认 false 使用 Content-Length) | false |
| `-vary` | Vary 头配置字符串，格式: "[\"Accept-Encoding\",\"User-Agent\"]" | "" |
| `-add-resp-header` | 添加响应头 (格式: "Header: Value"，可多次指定) | - |

## 请求头控制

除了命令行参数外，以下请求头也可以控制服务器行为：

### 客户端发送的请求头

| 请求头 | 描述 | 示例 |
|--------|------|------|
| `X-WYCDN-Request-ID` | 请求ID，用于日志追踪 | `test_123456` |
| `Range` | 范围请求 | `bytes=0-1023` |
| `Accept-Encoding` | 压缩方式 | `gzip, deflate` |
| `Host` | 虚拟主机名 | `example.com` |
| `Connection` | 连接控制 | `keep-alive` 或 `close` |
| `X-Resp-Add-Header` | 动态添加响应头（格式: "Header: Value"，支持多个用逗号分隔） | `Cache-Control: max-age=3600` 或 `Cache-Control: max-age=3600, X-Custom: value` |

### 服务器响应头

| 响应头 | 描述 | 示例 |
|--------|------|------|
| `X-Content-MD5` | 响应体MD5哈希值 | `d41d8cd98f00b204e9800998ecf8427e` |
| `Content-Length` | 响应体长度 | `1024` |
| `Content-Range` | 范围响应 | `bytes 0-1023/2048` |
| `Content-Encoding` | 压缩方式 | `gzip` |
| `Connection` | 连接控制 | `keep-alive` 或 `close` |
| `Vary` | 缓存vary头 | `Accept-Encoding` |
| `X-Debug-ReqHdr-*` | 调试响应头，显示原始请求头（格式: X-Debug-ReqHdr-{HeaderName}） | `X-Debug-ReqHdr-Host: example.com` |

## 使用示例

### 服务端示例

#### 基本服务端
```bash
./cache_press -mode=server -port=9000
```

#### 启用哈希校验和压缩的服务端
```bash
./cache_press -mode=server -port=9000 -enable-hash=true -pre-compress=true
```

#### 带自定义响应头的服务端
```bash
# 添加单个响应头
./cache_press -mode=server -port=9000 \
  -add-resp-header="Cache-Control: max-age=3600"

# 添加多个响应头（可多次使用）
./cache_press -mode=server -port=9000 \
  -add-resp-header="Cache-Control: max-age=3600" \
  -add-resp-header="X-Powered-By: cache_press" \
  -add-resp-header="X-Custom-Header: custom_value"

# 添加响应头并启用哈希校验
./cache_press -mode=server -port=9000 \
  -enable-hash=true \
  -add-resp-header="X-Content-MD5: d41d8cd98f00b204e9800998ecf8427e"

# 添加响应头并限制响应速率
./cache_press -mode=server -port=9000 \
  -resp-rate="5MB/s" \
  -add-resp-header="Cache-Control: max-age=3600" \
  -add-resp-header="X-Rate-Limited: true"
```

#### 限制响应速率的服务端
```bash
./cache_press -mode=server -port=9000 -resp-rate="10MB/s"
```

### 客户端示例

#### 基本客户端
```bash
./cache_press -mode=client -addr=192.168.1.100:8080 -conns=100 -qps=1000 -duration=60s
```

#### 高命中率的CDN测试
```bash
./cache_press -mode=client -addr=192.168.1.100:8080 \
  -conns=500 -qps=5000 -duration=300s \
  -hit-ratio=0.9 -url-count=100000
```

#### 带范围请求的测试
```bash
./cache_press -mode=client -addr=192.168.1.100:8080 \
  -range="[0-1023,1024-2047]" -range-random=true
```

#### 固定URL列表测试
```bash
./cache_press -mode=client -addr=192.168.1.100:8080 \
  -fixed-url="/path1.js,/path2.js,/path3.js" -hit-ratio=1.0
```

#### 使用 X-Resp-Add-Header 动态添加响应头
```bash
# 添加单个响应头（通过请求头）
curl -H "X-Resp-Add-Header: Cache-Control: max-age=3600" \
  http://192.168.1.100:8080/test.js

# 添加多个响应头（通过请求头，用逗号分隔）
curl -H "X-Resp-Add-Header: Cache-Control: max-age=3600, X-Powered-By: cache_press" \
  http://192.168.1.100:8080/test.js

# 使用 cache_press 客户端发送 X-Resp-Add-Header 请求头
# 需要通过 -req-header-file 参数配置
```

#### 使用请求头文件配置 X-Resp-Add-Header
```bash
# 创建请求头文件 req-header.txt
cat > req-header.txt << EOF
X-Resp-Add-Header: Cache-Control: max-age=3600
X-Resp-Add-Header: X-Powered-By: cache_press
X-Resp-Add-Header: X-Custom-Header: custom_value
EOF

# 使用请求头文件运行客户端
./cache_press -mode=client -addr=192.168.1.100:8080 \
  -req-header-file=req-header.txt -conns=100 -qps=1000 -duration=60s
```

## 配置文件示例

### 响应头文件示例 (`resp-header.txt`)
```
Cache-Control max-age=3600
X-Powered-By cache_press
Content-Type application/javascript
```

### 请求头文件示例 (`req-header.txt`)
```
User-Agent Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36
X-Custom-Header value1
```

## 高级用法

### 模拟不同大小的响应
```bash
# 50%的请求返回1KB响应，50%返回1MB响应
./cache_press -mode=client -addr=192.168.1.100:8080 -resp-size="[1024,1048576]" -disk-ratio=0.5
```

### 测试连接池性能
```bash
./cache_press -mode=client -addr=192.168.1.100:8080 \
  -conns=1000 -max-idle-conns=2000 -max-idle-conns-per-host=500 \
  -idle-conn-timeout=60s
```

### 测试哈希校验
```bash
# 服务端启用哈希校验
./cache_press -mode=server -port=9000 -enable-hash=true

# 客户端测试哈希校验
./cache_press -mode=client -addr=127.0.0.1:9000 -test-hash-failure=false
```

## 注意事项

1. 当使用 `-addr` 参数时，会忽略 `-host` 和 `-port` 参数
2. 客户端的 `-fixed-url` 参数优先级高于 `-url-count`
3. 服务端的 `-add-resp-header` 参数可以多次使用，后指定的会覆盖先指定的同名头
4. 当设置 `-resp-rate` 时，会自动计算并设置 `-send-bytes-per-interval` 和 `-send-interval-ms`

## 常见问题

### Q: 如何测试大文件传输？
A: 使用 `-resp-size` 参数设置较大的响应大小，例如 `-resp-size=10485760` (10MB)

### Q: 如何模拟不稳定的网络连接？
A: 使用 `-client-close-conn-prob` 和 `-server-close-conn-after-body-prob` 参数设置连接关闭概率

### Q: 如何测试缓存命中率对性能的影响？
A: 使用 `-hit-ratio` 参数设置不同的命中率，例如 `-hit-ratio=0.8` 表示80%的请求命中缓存

## 后续计划

1. 客户端和回源头部校验，对部分或所有头做一致性校验，或者配置排除某些头部
2. 源站打印日志，支持trace id
3. 支持body长度以及md5校验（分片缓存）

