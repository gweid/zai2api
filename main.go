package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 配置变量（支持环境变量覆盖）
var (
	UPSTREAM_URL     string
	DEFAULT_KEY      string
	UPSTREAM_TOKEN   string
	MODEL_NAME       string
	PORT             string
	DEBUG_MODE       bool
	THINK_TAGS_MODE  string
	ANON_TOKEN_ENABLED bool
)

// 伪装前端头部（来自抓包）
const (
	X_FE_VERSION   = "prod-fe-1.0.76"
	BROWSER_UA     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/139.0.0.0"
	SEC_CH_UA      = "\"Not;A=Brand\";v=\"99\", \"Edge\";v=\"139\""
	SEC_CH_UA_MOB  = "?0"
	SEC_CH_UA_PLAT = "\"Windows\""
	ORIGIN_BASE    = "https://chat.z.ai"
)

// 初始化配置
func init() {
	// 从环境变量读取配置，如果没有则使用默认值
	UPSTREAM_URL = getEnv("UPSTREAM_URL", "https://chat.z.ai/api/chat/completions")
	DEFAULT_KEY = getEnv("DEFAULT_KEY", "sk-123456")
	UPSTREAM_TOKEN = getEnv("UPSTREAM_TOKEN", "eyJ...")
	MODEL_NAME = getEnv("MODEL_NAME", "GLM-4.5")
	PORT = getEnv("PORT", ":3007")
	DEBUG_MODE = getEnvBool("DEBUG_MODE", true)
	THINK_TAGS_MODE = getEnv("THINK_TAGS_MODE", "think")
	ANON_TOKEN_ENABLED = getEnvBool("ANON_TOKEN_ENABLED", true)
}

// 获取环境变量，如果不存在则返回默认值
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// 获取布尔类型的环境变量
func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		boolValue, err := strconv.ParseBool(value)
		if err != nil {
			log.Printf("警告: 无法解析环境变量 %s=%s 为布尔值，使用默认值 %v", key, value, defaultValue)
			return defaultValue
		}
		return boolValue
	}
	return defaultValue
}

// OpenAI 请求结构
type OpenAIRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// 上游请求结构
type UpstreamRequest struct {
	Stream          bool                   `json:"stream"`
	Model           string                 `json:"model"`
	Messages        []Message              `json:"messages"`
	Params          map[string]interface{} `json:"params"`
	Features        map[string]interface{} `json:"features"`
	BackgroundTasks map[string]bool        `json:"background_tasks,omitempty"`
	ChatID          string                 `json:"chat_id,omitempty"`
	ID              string                 `json:"id,omitempty"`
	MCPServers      []string               `json:"mcp_servers,omitempty"`
	ModelItem       struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		OwnedBy string `json:"owned_by"`
	} `json:"model_item,omitempty"`
	ToolServers []string          `json:"tool_servers,omitempty"`
	Variables   map[string]string `json:"variables,omitempty"`
}

// OpenAI 响应结构
type OpenAIResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage,omitempty"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message,omitempty"`
	Delta        Delta   `json:"delta,omitempty"`
	FinishReason string  `json:"finish_reason,omitempty"`
}

type Delta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// 上游SSE响应结构
type UpstreamData struct {
	Type string `json:"type"`
	Data struct {
		DeltaContent string         `json:"delta_content"`
		EditContent  string         `json:"edit_content"`
		Phase        string         `json:"phase"`
		Done         bool           `json:"done"`
		Usage        Usage          `json:"usage,omitempty"`
		Error        *UpstreamError `json:"error,omitempty"`
		Inner        *struct {
			Error *UpstreamError `json:"error,omitempty"`
		} `json:"data,omitempty"`
	} `json:"data"`
	Error *UpstreamError `json:"error,omitempty"`
}

type UpstreamError struct {
	Detail string `json:"detail"`
	Code   int    `json:"code"`
}

// 模型列表响应
type ModelsResponse struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ContentProcessor 用于处理内容的状态管理
type ContentProcessor struct {
	historyPhase string
}

// NewContentProcessor 创建新的内容处理器
func NewContentProcessor() *ContentProcessor {
	return &ContentProcessor{
		historyPhase: "thinking",
	}
}

// ProcessContent 处理内容，与 Python 版本逻辑保持一致
func (cp *ContentProcessor) ProcessContent(content string, phase string) string {
	historyContent := content
	
	if content != "" && (phase == "thinking" || strings.Contains(content, "summary>")) {
		// 移除 details 标签
		detailsRe := regexp.MustCompile(`(?s)<details[^>]*?>.*?</details>`)
		content = detailsRe.ReplaceAllString(content, "")
		content = strings.ReplaceAll(content, "</thinking>", "")
		content = strings.ReplaceAll(content, "<Full>", "")
		content = strings.ReplaceAll(content, "</Full>", "")
		
		switch THINK_TAGS_MODE {
		case "think":
			if phase == "thinking" {
				content = strings.TrimPrefix(content, "> ")
				content = strings.ReplaceAll(content, "\n>", "\n")
				content = strings.TrimSpace(content)
			}
			
			// 移除 summary 标签
			summaryRe := regexp.MustCompile(`\n?<summary>.*?</summary>\n?`)
			content = summaryRe.ReplaceAllString(content, "")
			// 替换 details 为 think
			detailsOpenRe := regexp.MustCompile(`<details[^>]*>\n?`)
			content = detailsOpenRe.ReplaceAllString(content, "<think>\n\n")
			detailsCloseRe := regexp.MustCompile(`\n?</details>`)
			content = detailsCloseRe.ReplaceAllString(content, "\n\n</think>")
			
			if phase == "answer" {
				// 判断 </think> 后是否有内容
				re := regexp.MustCompile(`(?s)^(.*?</think>)(.*)$`)
				if matches := re.FindStringSubmatch(content); len(matches) == 3 {
					_, after := matches[1], matches[2]
					if strings.TrimSpace(after) != "" {
						// 回答休止：</think> 后有内容
						if cp.historyPhase == "thinking" {
							// 上条是思考 → 结束思考，加上回答
							content = fmt.Sprintf("\n\n</think>\n\n%s", strings.TrimLeft(after, "\n"))
						} else if cp.historyPhase == "answer" {
							// 上条是回答 → 清除所有
							content = ""
						}
					} else {
						// 思考休止：</think> 后没有内容 → 保留一个 </think>
						content = "\n\n</think>"
					}
				}
			}
			
		case "pure":
			if phase == "thinking" {
				summaryRe := regexp.MustCompile(`\n?<summary>.*?</summary>`)
				content = summaryRe.ReplaceAllString(content, "")
			}
			
			detailsOpenRe := regexp.MustCompile(`<details[^>]*>\n?`)
			content = detailsOpenRe.ReplaceAllString(content, `<details type="reasoning">`)
			detailsCloseRe := regexp.MustCompile(`\n?</details>`)
			content = detailsCloseRe.ReplaceAllString(content, "\n\n></details>")
			
			if phase == "answer" {
				// 判断 </details> 后是否有内容
				re := regexp.MustCompile(`(?s)^(.*?</details>)(.*)$`)
				if matches := re.FindStringSubmatch(content); len(matches) == 3 {
					_, after := matches[1], matches[2]
					if strings.TrimSpace(after) != "" {
						// 回答休止：</details> 后有内容
						if cp.historyPhase == "thinking" {
							// 上条是思考 → 结束思考，去除回答开头空格，加上回答
							content = fmt.Sprintf("\n\n%s", strings.TrimLeft(after, "\n"))
						} else if cp.historyPhase == "answer" {
							// 上条是回答 → 清除所有
							content = ""
						}
					} else {
						content = ""
					}
				}
			}
			// 清理 details 标签
			detailsCleanRe := regexp.MustCompile(`</?details[^>]*>`)
			content = detailsCleanRe.ReplaceAllString(content, "")
			
		case "raw":
			if phase == "thinking" {
				summaryRe := regexp.MustCompile(`\n?<summary>.*?</summary>`)
				content = summaryRe.ReplaceAllString(content, "")
			}
			
			detailsOpenRe := regexp.MustCompile(`<details[^>]*>\n?`)
			content = detailsOpenRe.ReplaceAllString(content, `<details type="reasoning" open><div>

`)
			detailsCloseRe := regexp.MustCompile(`\n?</details>`)
			content = detailsCloseRe.ReplaceAllString(content, "\n\n</div></details>")
			
			if phase == "answer" {
				// 判断 </details> 后是否有内容
				re := regexp.MustCompile(`(?s)^(.*?</details>)(.*)$`)
				if matches := re.FindStringSubmatch(content); len(matches) == 3 {
					before, after := matches[1], matches[2]
					if strings.TrimSpace(after) != "" {
						// 回答休止：</details> 后有内容
						if cp.historyPhase == "thinking" {
							// 上条是思考 → 结束思考，加上回答
							content = fmt.Sprintf("\n\n</details>\n\n%s", strings.TrimLeft(after, "\n"))
						} else if cp.historyPhase == "answer" {
							// 上条是回答 → 清除所有
							content = ""
						}
					} else {
						// 思考休止: </details> 后没有内容 → 加入 summary + </details>
						summaryRe := regexp.MustCompile(`(?s)<summary>.*?</summary>`)
						durationRe := regexp.MustCompile(`duration="(\d+)"`)
						
						if summaryMatch := summaryRe.FindString(before); summaryMatch != "" {
							content = fmt.Sprintf("\n\n</div>%s</details>\n\n", summaryMatch)
						} else if durationMatch := durationRe.FindStringSubmatch(before); len(durationMatch) > 1 {
							duration := durationMatch[1]
							content = fmt.Sprintf("\n\n</div><summary>Thought for %s seconds</summary></details>\n\n", duration)
						} else {
							content = "\n\n</div></details>"
						}
					}
				}
			}
		}
	}
	
	// 调试日志
	if historyContent != content {
		debugLog("R 内容: %s %s", phase, historyContent)
		debugLog("W 内容: %s %s", phase, content)
	} else {
		debugLog("R 内容: %s %s", phase, historyContent)
	}
	
	cp.historyPhase = phase
	return content
}

// ExtractContent 从上游数据中提取内容
func (cp *ContentProcessor) ExtractContent(data UpstreamData) string {
	phase := data.Data.Phase
	delta := data.Data.DeltaContent
	edit := data.Data.EditContent
	
	content := delta
	if content == "" {
		content = edit
	}
	
	if content != "" && (phase == "answer" || phase == "thinking") {
		processed := cp.ProcessContent(content, phase)
		if processed == "" {
			return ""
		}
		return processed
	}
	
	return content
}

// debug日志函数
func debugLog(format string, args ...interface{}) {
	if DEBUG_MODE {
		log.Printf("[DEBUG] "+format, args...)
	}
}

// 获取匿名token（每次对话使用不同token，避免共享记忆）
func getAnonymousToken() (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", ORIGIN_BASE+"/api/v1/auths/", nil)
	if err != nil {
		return "", err
	}
	// 伪装浏览器头
	req.Header.Set("User-Agent", BROWSER_UA)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("X-FE-Version", X_FE_VERSION)
	req.Header.Set("sec-ch-ua", SEC_CH_UA)
	req.Header.Set("sec-ch-ua-mobile", SEC_CH_UA_MOB)
	req.Header.Set("sec-ch-ua-platform", SEC_CH_UA_PLAT)
	req.Header.Set("Origin", ORIGIN_BASE)
	req.Header.Set("Referer", ORIGIN_BASE+"/")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anon token status=%d", resp.StatusCode)
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.Token == "" {
		return "", fmt.Errorf("anon token empty")
	}
	return body.Token, nil
}

func main() {
	http.HandleFunc("/v1/models", handleModels)
	http.HandleFunc("/v1/chat/completions", handleChatCompletions)
	http.HandleFunc("/", handleOptions)

	log.Printf("OpenAI兼容API服务器启动在端口%s", PORT)
	log.Printf("模型: %s", MODEL_NAME)
	log.Printf("上游: %s", UPSTREAM_URL)
	log.Printf("Debug模式: %v", DEBUG_MODE)
	log.Fatal(http.ListenAndServe(PORT, nil))
}

func handleOptions(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
}

func handleModels(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// 验证API Key
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		debugLog("缺少或无效的Authorization头")
		http.Error(w, "Missing or invalid Authorization header", http.StatusUnauthorized)
		return
	}

	apiKey := strings.TrimPrefix(authHeader, "Bearer ")
	if apiKey != DEFAULT_KEY {
		debugLog("无效的API key: %s", apiKey)
		http.Error(w, "Invalid API key", http.StatusUnauthorized)
		return
	}

	response := ModelsResponse{
		Object: "list",
		Data: []Model{
			{
				ID:      MODEL_NAME,
				Object:  "model",
				Created: time.Now().Unix(),
				OwnedBy: "z.ai",
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	debugLog("收到chat completions请求")

	// 验证API Key
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		debugLog("缺少或无效的Authorization头")
		http.Error(w, "Missing or invalid Authorization header", http.StatusUnauthorized)
		return
	}

	apiKey := strings.TrimPrefix(authHeader, "Bearer ")
	if apiKey != DEFAULT_KEY {
		debugLog("无效的API key: %s", apiKey)
		http.Error(w, "Invalid API key", http.StatusUnauthorized)
		return
	}

	debugLog("API key验证通过")

	// 解析请求
	var req OpenAIRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		debugLog("JSON解析失败: %v", err)
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	debugLog("请求解析成功 - 模型: %s, 流式: %v, 消息数: %d", req.Model, req.Stream, len(req.Messages))

	// 生成会话相关ID
	chatID := fmt.Sprintf("%d-%d", time.Now().UnixNano(), time.Now().Unix())
	msgID := fmt.Sprintf("%d", time.Now().UnixNano())

	// 构造上游请求
	upstreamReq := UpstreamRequest{
		Stream:   true, // 总是使用流式从上游获取
		ChatID:   chatID,
		ID:       msgID,
		Model:    "0727-360B-API", // 上游实际模型ID
		Messages: req.Messages,
		Params:   map[string]interface{}{},
		Features: map[string]interface{}{
			"enable_thinking": true,
		},
		BackgroundTasks: map[string]bool{
			"title_generation": false,
			"tags_generation":  false,
		},
		MCPServers: []string{},
		ModelItem: struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			OwnedBy string `json:"owned_by"`
		}{ID: "0727-360B-API", Name: "GLM-4.5", OwnedBy: "openai"},
		ToolServers: []string{},
		Variables: map[string]string{
			"{{USER_NAME}}":        "User",
			"{{USER_LOCATION}}":    "Unknown",
			"{{CURRENT_DATETIME}}": time.Now().Format("2006-01-02 15:04:05"),
		},
	}

	// 选择本次对话使用的token
	authToken := UPSTREAM_TOKEN
	if ANON_TOKEN_ENABLED {
		if t, err := getAnonymousToken(); err == nil {
			authToken = t
			debugLog("匿名token获取成功: %s...", func() string {
				if len(t) > 10 {
					return t[:10]
				}
				return t
			}())
		} else {
			debugLog("匿名token获取失败，回退固定token: %v", err)
		}
	}

	// 调用上游API
	if req.Stream {
		handleStreamResponseWithIDs(w, upstreamReq, chatID, authToken)
	} else {
		handleNonStreamResponseWithIDs(w, upstreamReq, chatID, authToken)
	}
}

func callUpstreamWithHeaders(upstreamReq UpstreamRequest, refererChatID string, authToken string) (*http.Response, error) {
	reqBody, err := json.Marshal(upstreamReq)
	if err != nil {
		debugLog("上游请求序列化失败: %v", err)
		return nil, err
	}

	debugLog("调用上游API: %s", UPSTREAM_URL)
	debugLog("上游请求体: %s", string(reqBody))

	req, err := http.NewRequest("POST", UPSTREAM_URL, bytes.NewBuffer(reqBody))
	if err != nil {
		debugLog("创建HTTP请求失败: %v", err)
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", BROWSER_UA)
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("Accept-Language", "zh-CN")
	req.Header.Set("sec-ch-ua", SEC_CH_UA)
	req.Header.Set("sec-ch-ua-mobile", SEC_CH_UA_MOB)
	req.Header.Set("sec-ch-ua-platform", SEC_CH_UA_PLAT)
	req.Header.Set("X-FE-Version", X_FE_VERSION)
	req.Header.Set("Origin", ORIGIN_BASE)
	req.Header.Set("Referer", ORIGIN_BASE+"/c/"+refererChatID)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		debugLog("上游请求失败: %v", err)
		return nil, err
	}

	debugLog("上游响应状态: %d %s", resp.StatusCode, resp.Status)
	return resp, nil
}

func handleStreamResponseWithIDs(w http.ResponseWriter, upstreamReq UpstreamRequest, chatID string, authToken string) {
	debugLog("开始处理流式响应 (chat_id=%s)", chatID)

	resp, err := callUpstreamWithHeaders(upstreamReq, chatID, authToken)
	if err != nil {
		debugLog("调用上游失败: %v", err)
		http.Error(w, "Failed to call upstream", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		debugLog("上游返回错误状态: %d", resp.StatusCode)
		// 读取错误响应体
		if DEBUG_MODE {
			body, _ := io.ReadAll(resp.Body)
			debugLog("上游错误响应: %s", string(body))
		}
		http.Error(w, "Upstream error", http.StatusBadGateway)
		return
	}

	// 创建内容处理器
	processor := NewContentProcessor()

	// 设置SSE头部
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// 生成响应ID
	completionID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())

	// 发送第一个chunk（role）
	firstChunk := OpenAIResponse{
		ID:      completionID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   MODEL_NAME,
		Choices: []Choice{
			{
				Index: 0,
				Delta: Delta{Role: "assistant"},
			},
		},
	}
	writeSSEChunk(w, firstChunk)
	flusher.Flush()

	// 读取上游SSE流
	debugLog("开始读取上游SSE流")
	scanner := bufio.NewScanner(resp.Body)
	lineCount := 0

	for scanner.Scan() {
		line := scanner.Text()
		lineCount++

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		dataStr := strings.TrimPrefix(line, "data: ")
		if dataStr == "" {
			continue
		}

		debugLog("收到SSE数据 (第%d行): %s", lineCount, dataStr)

		var upstreamData UpstreamData
		if err := json.Unmarshal([]byte(dataStr), &upstreamData); err != nil {
			debugLog("SSE数据解析失败: %v", err)
			continue
		}

		// 错误检测（data.error 或 data.data.error 或 顶层error）
		if (upstreamData.Error != nil) || (upstreamData.Data.Error != nil) || (upstreamData.Data.Inner != nil && upstreamData.Data.Inner.Error != nil) {
			errObj := upstreamData.Error
			if errObj == nil {
				errObj = upstreamData.Data.Error
			}
			if errObj == nil && upstreamData.Data.Inner != nil {
				errObj = upstreamData.Data.Inner.Error
			}
			debugLog("上游错误: code=%d, detail=%s", errObj.Code, errObj.Detail)
			// 结束下游流
			endChunk := OpenAIResponse{
				ID:      completionID,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   MODEL_NAME,
				Choices: []Choice{{Index: 0, Delta: Delta{}, FinishReason: "stop"}},
			}
			writeSSEChunk(w, endChunk)
			fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()
			break
		}

		debugLog("解析成功 - 类型: %s, 阶段: %s, delta长度: %d, edit长度: %d, 完成: %v",
			upstreamData.Type, upstreamData.Data.Phase,
			len(upstreamData.Data.DeltaContent), len(upstreamData.Data.EditContent),
			upstreamData.Data.Done)

		// 使用内容处理器提取和处理内容
		content := processor.ExtractContent(upstreamData)
		if content != "" {
			debugLog("发送内容(%s): %s", upstreamData.Data.Phase, content)
			chunk := OpenAIResponse{
				ID:      completionID,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   MODEL_NAME,
				Choices: []Choice{
					{
						Index: 0,
						Delta: Delta{Content: content},
					},
				},
			}
			writeSSEChunk(w, chunk)
			flusher.Flush()
		}

		// 检查是否结束
		if upstreamData.Data.Done || upstreamData.Data.Phase == "done" {
			debugLog("检测到流结束信号")
			// 发送结束chunk
			endChunk := OpenAIResponse{
				ID:      completionID,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   MODEL_NAME,
				Choices: []Choice{
					{
						Index:        0,
						Delta:        Delta{},
						FinishReason: "stop",
					},
				},
			}
			writeSSEChunk(w, endChunk)
			flusher.Flush()

			// 发送[DONE]
			fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()
			debugLog("流式响应完成，共处理%d行", lineCount)
			break
		}
	}

	if err := scanner.Err(); err != nil {
		debugLog("扫描器错误: %v", err)
	}
}

func writeSSEChunk(w http.ResponseWriter, chunk OpenAIResponse) {
	// 使用自定义 encoder 避免 HTML 转义
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false) // 关闭 HTML 转义
	encoder.Encode(chunk)
	// 移除末尾的换行符（Encode 会添加）
	data := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	fmt.Fprintf(w, "data: %s\n\n", data)
}

func handleNonStreamResponseWithIDs(w http.ResponseWriter, upstreamReq UpstreamRequest, chatID string, authToken string) {
	debugLog("开始处理非流式响应 (chat_id=%s)", chatID)

	resp, err := callUpstreamWithHeaders(upstreamReq, chatID, authToken)
	if err != nil {
		debugLog("调用上游失败: %v", err)
		http.Error(w, "Failed to call upstream", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		debugLog("上游返回错误状态: %d", resp.StatusCode)
		// 读取错误响应体
		if DEBUG_MODE {
			body, _ := io.ReadAll(resp.Body)
			debugLog("上游错误响应: %s", string(body))
		}
		http.Error(w, "Upstream error", http.StatusBadGateway)
		return
	}

	// 创建内容处理器
	processor := NewContentProcessor()
	
	// 收集完整响应
	var fullContent strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	debugLog("开始收集完整响应内容")

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		dataStr := strings.TrimPrefix(line, "data: ")
		if dataStr == "" {
			continue
		}

		var upstreamData UpstreamData
		if err := json.Unmarshal([]byte(dataStr), &upstreamData); err != nil {
			continue
		}

		// 使用内容处理器提取和处理内容
		content := processor.ExtractContent(upstreamData)
		if content != "" {
			fullContent.WriteString(content)
		}

		if upstreamData.Data.Done || upstreamData.Data.Phase == "done" {
			debugLog("检测到完成信号，停止收集")
			break
		}
	}

	finalContent := fullContent.String()
	debugLog("内容收集完成，最终长度: %d", len(finalContent))

	// 生成响应ID
	completionID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())

	// 构造完整响应
	response := OpenAIResponse{
		ID:      completionID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   MODEL_NAME,
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:    "assistant",
					Content: finalContent,
				},
				FinishReason: "stop",
			},
		},
		Usage: Usage{
			PromptTokens:     0,
			CompletionTokens: 0,
			TotalTokens:      0,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
	debugLog("非流式响应发送完成")
}
