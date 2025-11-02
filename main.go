package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"time"
)

const (
	FeishuWebHook = ""  // 自行申请得到的信息
	FeishuSecret  = ""  // 自行申请得到的信息

	BinanceApiKey    = ""  // 自行申请得到的信息
	BinanceSecretKey = ""  // 自行申请得到的信息
	BinanceBaseUrl   = "https://fapi.binance.com"
)

// FeishuRobotMsg 飞书机器人消息
type FeishuRobotMsg struct {
	Timestamp string        `json:"timestamp"`
	Sign      string        `json:"sign"`
	MsgType   string        `json:"msg_type"`
	Content   FeishuContent `json:"content"`
}

type FeishuContent struct {
	Text string `json:"text"`
}

var logger *log.Logger

func initLogger() {
	file, err := os.OpenFile("strategy.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Println("无法创建日志文件:", err)
		os.Exit(1)
	}
	logger = log.New(file, "", log.LstdFlags)
}

func GetFeishuSignature(timeUnixNano int64) (string, error) {
	signature, err := generateSignature(timeUnixNano, FeishuSecret)
	if err != nil {
		return "", fmt.Errorf("generateSignature err: %s\n", err)
	}
	return signature, nil
}

func GetTimestamp() int64 {
	return time.Now().Unix()
}

func generateSignature(timestamp int64, secret string) (string, error) {
	stringToSign := fmt.Sprintf("%v", timestamp) + "\n" + secret
	var data []byte
	h := hmac.New(sha256.New, []byte(stringToSign))
	_, err := h.Write(data)
	if err != nil {
		return "", err
	}
	signature := base64.StdEncoding.EncodeToString(h.Sum(nil))
	return signature, nil
}

func createClient() *http.Client {
	client := &http.Client{}
	os := runtime.GOOS
	switch os {
	case "windows":
		proxy := func(_ *http.Request) (*url.URL, error) {
			return url.Parse("http://127.0.0.1:7890")
		}
		return &http.Client{
			Transport: &http.Transport{
				Proxy: proxy,
			},
			Timeout: 2 * time.Second,
		}

	case "linux":
		connectTimeout := 2000
		readTimeout := 2000
		return &http.Client{
			Transport: &http.Transport{
				// 配置TLS客户端的行为
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // 跳过证书验证，慎用
				// 配置在建立连接时的超时时间和死线时间
				DialContext: (&net.Dialer{
					Timeout:  time.Duration(connectTimeout) * time.Millisecond,              // 连接超时时间
					Deadline: time.Now().Add(time.Duration(readTimeout) * time.Millisecond), // 连接的死线时间
				}).DialContext,
				// 禁用连接复用
				DisableKeepAlives: true,
			},
			Timeout: 2 * time.Second,
		}
	}
	return client
}

func SendMsg(requestPath, method string, body []byte, headers map[string]string) ([]byte, error) {
	return sendReq(requestPath, method, body, headers)
}

func sendReq(requestPath, method string, body []byte, headers map[string]string) ([]byte, error) {
	var respBody []byte
	client := createClient()
	request, err := http.NewRequest(method, requestPath, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	// 设置自定义的请求头部
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	resp, err := client.Do(request)
	if err != nil {
		fmt.Println(err)
		return respBody, err
	}
	defer func() {
		err = resp.Body.Close()
		if err != nil {
			fmt.Println("close err: ", err)
		}
	}()
	respBody, _ = ioutil.ReadAll(resp.Body)
	return respBody, nil
}

func SendMsgToFeishu(sendMsg string) error {
	timestamp := GetTimestamp()
	fmt.Println(timestamp)
	signature, err := GetFeishuSignature(timestamp)
	if err != nil {
		fmt.Println("获取飞书签名失败")
	}
	robotMsg := FeishuRobotMsg{
		Timestamp: fmt.Sprintf("%v", timestamp),
		Sign:      signature,
		MsgType:   "text",
		Content: FeishuContent{
			Text: sendMsg,
		},
	}
	headers := make(map[string]string)
	headers["Content-Type"] = "application/json"
	requestBody, err := json.Marshal(robotMsg)
	if err != nil {
		fmt.Println("FeishuRobotMsg GetMsg err")
	}
	response, err := SendMsg(FeishuWebHook, "POST",
		bytes.NewBuffer(requestBody).Bytes(), headers)
	if err != nil {
		return fmt.Errorf(err.Error())
	}
	fmt.Println(string(response))
	return nil
}

func fetchKlines(symbol, interval string, limit int, proxyAddr string) ([][]interface{}, error) {
	apiURL := fmt.Sprintf("https://fapi.binance.com/fapi/v1/klines?symbol=%s&interval=%s&limit=%d", symbol, interval, limit)
	client := &http.Client{}

	// 发起请求
	resp, err := client.Get(apiURL)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	var klines [][]interface{}
	if err := json.Unmarshal(body, &klines); err != nil {
		return nil, fmt.Errorf("解析 JSON 失败: %v", err)
	}
	return klines, nil
}

func calculateEMA(prices []float64, period int) []float64 {
	var ema []float64
	if len(prices) < period {
		return ema
	}
	sum := 0.0
	for i := 0; i < period; i++ {
		sum += prices[i]
	}
	sma := sum / float64(period)
	ema = append(ema, sma)

	multiplier := 2.0 / float64(period+1)
	for i := period; i < len(prices); i++ {
		prev := ema[len(ema)-1]
		current := (prices[i]-prev)*multiplier + prev
		ema = append(ema, current)
	}
	return ema
}

func analyze_one_hour() {
	var (
		isLong    = false
		isShort   = false
		longStop  float64 // 多：止损价
		shortStop float64 // 空：止损价
		longOpen  float64 // 多：开仓价
		shortOpen float64 // 空：开仓价
	)

	proxy := "http://127.0.0.1:1080"
	// 获取 1h K线并计算 EMA-28
	klines1h, err := fetchKlines("ETHUSDT", "1h", 200, proxy)
	if err != nil {
		logger.Println("获取 1h K线失败:", err)
		return
	}

	var closePrices []float64
	var openPrices []float64
	for _, k := range klines1h {
		openPriceStr := k[1].(string)
		closePriceStr := k[4].(string)

		oPrice, _ := strconv.ParseFloat(openPriceStr, 64)
		cPrice, _ := strconv.ParseFloat(closePriceStr, 64)

		openPrices = append(openPrices, oPrice)
		closePrices = append(closePrices, cPrice)
	}
	ema28 := calculateEMA(closePrices, 28)
	if len(ema28) == 0 {
		logger.Printf("EMA 数据不足")
		return
	}

	currentPrice := closePrices[len(closePrices)-1]
	currentOpenPrice := openPrices[len(openPrices)-1]
	currentEMA := ema28[len(ema28)-1]

	klines15m, err := fetchKlines("ETHUSDT", "15m", 4, proxy)
	if err != nil {
		logger.Println("获取 15m K线失败:", err)
		return
	}
	lastKline := klines1h[len(klines1h)-1]
	lastOpen, _ := strconv.ParseFloat(lastKline[1].(string), 64)
	lastClose, _ := strconv.ParseFloat(lastKline[4].(string), 64)

	// 下降
	var isDownCrossEma bool = (currentOpenPrice > currentEMA && currentPrice < currentEMA)
	// 上升
	var isUpCrossEma bool = (currentOpenPrice < currentEMA && currentPrice > currentEMA)

	if isUpCrossEma &&
		!isLong {
		for _, k := range klines15m {
			open, _ := strconv.ParseFloat(k[1].(string), 64)
			close, _ := strconv.ParseFloat(k[4].(string), 64)
			timestamp := int64(k[0].(float64))
			t := time.UnixMilli(timestamp).In(time.FixedZone("CST", 8*3600))
			status := ""
			if open < close {
				count++
				status = "上涨"
			}
			logger.Printf("1小时趋势, %s -> Open: %.2f, Close: %.2f [%s]\n", t.Format("2006/01/02 15:04:05"), open, close, status)
		}
	} else if isDownCrossEma &&
		!isShort {
		for _, k := range klines15m {
			open, _ := strconv.ParseFloat(k[1].(string), 64)
			close, _ := strconv.ParseFloat(k[4].(string), 64)
			timestamp := int64(k[0].(float64))
			t := time.UnixMilli(timestamp).In(time.FixedZone("CST", 8*3600))
			status := ""
			if open > close {
				count++
				status = "下跌"
			}
			logger.Printf("1小时趋势, %s -> Open: %.2f, Close: %.2f [%s]\n", t.Format("2006/01/02 15:04:05"), open, close, status)
		}
	} 
  
	// 有多单持仓
	if isLong {
		// 不断调整止损价格
		if longOpen < currentPrice {
			longStop = lastOpen
			logger.Printf("1小时趋势, 多单持仓中, 当前止损价格调整为: %f\n", longStop)
			SendMsgToFeishu(fmt.Sprintf("1小时趋势, 多单持仓中, 当前价格：%f, 止损价格调整为: %f\n", currentPrice, longStop))
		}

		if longStop > currentPrice {
			isLong = false
			logger.Printf("1小时趋势, 多单平仓, 价差: %f\n", longStop-longOpen)
			SendMsgToFeishu(fmt.Sprintf("1小时趋势, 多单平仓, 当前价格：%f, 价差: %f\n", currentPrice, longStop-longOpen))
		}
	}
	// 有空单持仓
	if isShort {
		// 不断调整止损价格
		if shortOpen > currentPrice {
			shortStop = lastOpen
			logger.Printf("1小时趋势, 空单持仓中, 当前止损价格调整为: %f\n", shortStop)
			SendMsgToFeishu(fmt.Sprintf("1小时趋势, 空单持仓中, 当前价格：%f, 当前止损价格调整为: %f\n", currentPrice, shortStop))
		}
		if shortStop < currentPrice {
			isShort = false
			logger.Printf("1小时趋势, 空单平仓, 价差: %f\n", shortOpen-longStop)
			SendMsgToFeishu(fmt.Sprintf("1小时趋势, 空单平仓, 当前价格：%f, 价差: %f\n", currentPrice, shortOpen-longStop))
		}

	}
}

func server_health() {
	SendMsgToFeishu(fmt.Sprintf("%s server is alive", time.Now().Format("2006/01/02 15:04:05")))
}

func main() {
	fmt.Println("server is starting.")
	initLogger()

	timer3minute := time.NewTicker(3 * time.Minute)
	timer6hour := time.NewTicker(6 * time.Hour)

	// 立即执行一次
	analyze_one_hour()

	for {
		select {
		case <-timer3minute.C:
			go analyze_one_hour()
		case <-timer6hour.C:
			go server_health()
		}
	}
}
