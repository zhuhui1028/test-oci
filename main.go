/*
  甲骨文云API文档
  https://docs.oracle.com/en-us/iaas/api/#/en/iaas/20160918/

  实例:
  https://docs.oracle.com/en-us/iaas/api/#/en/iaas/20160918/Instance/
  VCN:
  https://docs.oracle.com/en-us/iaas/api/#/en/iaas/20160918/Vcn/
  Subnet:
  https://docs.oracle.com/en-us/iaas/api/#/en/iaas/20160918/Subnet/
  VNIC:
  https://docs.oracle.com/en-us/iaas/api/#/en/iaas/20160918/Vnic/
  VnicAttachment:
  https://docs.oracle.com/en-us/iaas/api/#/en/iaas/20160918/VnicAttachment/
  私有IP
  https://docs.oracle.com/en-us/iaas/api/#/en/iaas/20160918/PrivateIp/
  公共IP
  https://docs.oracle.com/en-us/iaas/api/#/en/iaas/20160918/PublicIp/
  IPv6
  https://docs.oracle.com/en-us/iaas/api/#/en/iaas/20160918/Ipv6/
  用户
  https://docs.oracle.com/en-us/iaas/api/#/en/identity/20160918/User/
  监控
  https://docs.oracle.com/en-us/iaas/api/#/en/monitoring/20180401/MetricData/SummarizeMetricsData

  获取可用性域
  https://docs.oracle.com/en-us/iaas/api/#/en/identity/20160918/AvailabilityDomain/ListAvailabilityDomains
*/
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
	"github.com/oracle/oci-go-sdk/v65/example/helpers"
	"github.com/oracle/oci-go-sdk/v65/identity"
	"github.com/oracle/oci-go-sdk/v65/monitoring"
	"gopkg.in/ini.v1"
)

const (
	defConfigFilePath = "./oci-help.ini"
	IPsFilePrefix     = "IPs"
	timeLayout        = "2006-01-02 15:04:05"
)

// 全局应用配置
var (
	appConfig struct {
		proxy          string
		token          string
		chat_id        string
		cmd            string
		sendMessageUrl string
		editMessageUrl string
		getUpdatesUrl  string
		each           bool
		lastUpdateId   int
	}
	ctx             = context.Background()
	cfg             *ini.File
	taskManager     *TaskManager
	selectedTenants = make(map[int64]string) // 存储每个聊天的已选租户
	userNextAction  = make(map[int64]string) // 存储用户的下一个预期操作
	mu              sync.RWMutex             // 用于保护并发访问map

	// 新增: 用于缓存每个聊天的实例列表
	instanceListCache  = make(map[int64][]core.Instance)
	instanceCacheMutex sync.RWMutex
)

// 单个OCI账户的配置
type Oracle struct {
	User         string `ini:"user"`
	Fingerprint  string `ini:"fingerprint"`
	Tenancy      string `ini:"tenancy"`
	Region       string `ini:"region"`
	Key_file     string `ini:"key_file"`
	Key_password string `ini:"key_password"`
}

// INI文件中的实例配置参数
type Instance struct {
	AvailabilityDomain     string  `ini:"availabilityDomain"`
	SSH_Public_Key         string  `ini:"ssh_authorized_key"`
	VcnDisplayName         string  `ini:"vcnDisplayName"`
	SubnetDisplayName      string  `ini:"subnetDisplayName"`
	Shape                  string  `ini:"shape"`
	OperatingSystem        string  `ini:"OperatingSystem"`
	OperatingSystemVersion string  `ini:"OperatingSystemVersion"`
	InstanceDisplayName    string  `ini:"instanceDisplayName"`
	Ocpus                  float32 `ini:"cpus"`
	MemoryInGBs            float32 `ini:"memoryInGBs"`
	Burstable              string  `ini:"burstable"`
	BootVolumeSizeInGBs    int64   `ini:"bootVolumeSizeInGBs"`
	Sum                    int32   `ini:"sum"`
	Each                   int32   `ini:"each"`
	Retry                  int32   `ini:"retry"`
	CloudInit              string  `ini:"cloud-init"`
	MinTime                int32   `ini:"minTime"`
	MaxTime                int32   `ini:"maxTime"`
}

// Telegram消息结构体
type Message struct {
	OK          bool `json:"ok"`
	Result      `json:"result"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
}
type Result struct {
	MessageId int `json:"message_id"`
}

// OciClients 包含所有必需的OCI服务客户端
type OciClients struct {
	Compute    core.ComputeClient
	Network    core.VirtualNetworkClient
	Storage    core.BlockstorageClient
	Identity   identity.IdentityClient
	Monitoring monitoring.MonitoringClient
	Provider   common.ConfigurationProvider
}

// App 包含应用程序的状态
type App struct {
	clients             *OciClients
	oracleConfig        Oracle
	oracleSection       *ini.Section
	oracleSectionName   string
	availabilityDomains []identity.AvailabilityDomain
	oracleSections      []*ini.Section
	instanceBaseSection *ini.Section
}

// TenantStatus 包含单个租户凭证检查的结果
type TenantStatus struct {
	Name    string
	Status  string
	Message string
}

// ############# Telegram Bot API 结构体 #############
type TgUpdate struct {
	UpdateId      int            `json:"update_id"`
	Message       *TgMessage     `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type TgMessage struct {
	MessageId int    `json:"message_id"`
	Text      string `json:"text"`
	Chat      struct {
		Id int64 `json:"id"`
	} `json:"chat"`
	From struct {
		Id int64 `json:"id"`
	} `json:"from"`
}

type CallbackQuery struct {
	Id      string     `json:"id"`
	From    struct {
		Id int64 `json:"id"`
	} `json:"from"`
	Message *TgMessage `json:"message"`
	Data    string     `json:"data"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

// ############# 任务管理结构体 #############
type CreationTask struct {
	ID                   string
	TenantName           string
	InstanceTemplate     string
	Status               string
	StartTime            time.Time
	Attempts             int32
	SuccessCount         int32
	TotalCount           int32
	cancelFunc           context.CancelFunc
	mu                   sync.RWMutex
	LastMessage          string
	LastMessageTimestamp time.Time
}

func (t *CreationTask) UpdateStatus(status string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Status = status
}

func (t *CreationTask) GetStatus() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return fmt.Sprintf("任务ID: `%s`\n租户: %s\n模版: %s\n状态: %s\n进度: %d/%d\n尝试次数: %d\n运行时长: %s",
		t.ID, t.TenantName, t.InstanceTemplate, t.Status, t.SuccessCount, t.TotalCount, t.Attempts, fmtDuration(time.Since(t.StartTime)))
}

type TaskManager struct {
	mu    sync.Mutex
	tasks map[string]*CreationTask
}

func NewTaskManager() *TaskManager {
	return &TaskManager{
		tasks: make(map[string]*CreationTask),
	}
}

func (tm *TaskManager) Add(task *CreationTask) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.tasks[task.ID] = task
}

func (tm *TaskManager) Get(id string) (*CreationTask, bool) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	task, ok := tm.tasks[id]
	return task, ok
}

func (tm *TaskManager) Remove(id string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	delete(tm.tasks, id)
}

func (tm *TaskManager) List() []*CreationTask {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	var tasks []*CreationTask
	for _, task := range tm.tasks {
		tasks = append(tasks, task)
	}
	// 按开始时间排序
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].StartTime.Before(tasks[j].StartTime)
	})
	return tasks
}

func main() {
	var configFilePath string
	var botMode bool
	flag.StringVar(&configFilePath, "config", defConfigFilePath, "配置文件路径")
	flag.StringVar(&configFilePath, "c", defConfigFilePath, "配置文件路径 (简写)")
	flag.BoolVar(&botMode, "bot", false, "以Telegram Bot模式启动")
	flag.Parse()

	var err error
	cfg, err = ini.Load(configFilePath)
	helpers.FatalIfError(err)

	loadAppConfig(cfg)
	rand.Seed(time.Now().UnixNano())

	if botMode {
		if appConfig.token == "" {
			fmt.Println("\033[1;31m错误: Bot模式需要设置Telegram token。\033[0m")
			os.Exit(1)
		}
		taskManager = NewTaskManager() // 初始化任务管理器
		fmt.Println("以Telegram Bot模式启动...")
		startBot()
	} else {
		app := &App{}
		app.loadOracleSections(cfg)
		app.run()
	}
}

func loadAppConfig(cfg *ini.File) {
	defSec := cfg.Section(ini.DefaultSection)
	appConfig.proxy = defSec.Key("proxy").Value()
	appConfig.token = defSec.Key("token").Value()
	appConfig.chat_id = defSec.Key("chat_id").Value()
	appConfig.cmd = defSec.Key("cmd").Value()
	appConfig.each, _ = defSec.Key("EACH").Bool()
	if appConfig.token != "" {
		apiBase := "https://api.telegram.org/bot" + appConfig.token
		appConfig.sendMessageUrl = apiBase + "/sendMessage"
		appConfig.editMessageUrl = apiBase + "/editMessageText"
		appConfig.getUpdatesUrl = apiBase + "/getUpdates"
	}
}

func (app *App) loadOracleSections(cfg *ini.File) {
	app.oracleSections = []*ini.Section{}
	for _, sec := range cfg.Sections() {
		if len(sec.ParentKeys()) == 0 {
			if sec.Key("user").Value() != "" && sec.Key("fingerprint").Value() != "" &&
				sec.Key("tenancy").Value() != "" && sec.Key("region").Value() != "" &&
				sec.Key("key_file").Value() != "" {
				app.oracleSections = append(app.oracleSections, sec)
			}
		}
	}
	if len(app.oracleSections) == 0 {
		fmt.Printf("\033[1;31m未找到正确的配置信息, 请参考链接文档配置相关信息。链接: https://github.com/lemoex/oci-help\033[0m\n")
		os.Exit(1)
	}
	app.instanceBaseSection = cfg.Section("INSTANCE")
}

func (app *App) run() {
	for {
		oracleSection, exit := app.selectOracleAccount()
		if exit {
			return
		}

		err := app.initializeClients(oracleSection)
		if err != nil {
			printlnErr("初始化客户端失败", err.Error())
			continue
		}

		app.showMainMenu()
	}
}

// ############# Telegram Bot 核心逻辑 #############

func getTgClient() *http.Client {
	client := &http.Client{Timeout: 40 * time.Second}
	if appConfig.proxy != "" {
		proxyURL, err := url.Parse(appConfig.proxy)
		if err != nil {
			printlnErr("代理URL解析失败", err.Error())
		} else {
			client.Transport = &http.Transport{Proxy: http.ProxyURL(proxyURL)}
		}
	}
	return client
}

func startBot() {
	fmt.Printf("Bot正在监听来自Chat ID: %s 的消息...\n", appConfig.chat_id)
	for {
		updates, err := getUpdates()
		if err != nil {
			printlnErr("获取TG更新失败", err.Error())
			time.Sleep(10 * time.Second)
			continue
		}

		for _, update := range updates {
			handleUpdate(update)
			appConfig.lastUpdateId = update.UpdateId + 1
		}

		time.Sleep(2 * time.Second)
	}
}

func getUpdates() ([]TgUpdate, error) {
	client := getTgClient()
	resp, err := client.Get(fmt.Sprintf("%s?offset=%d&timeout=30", appConfig.getUpdatesUrl, appConfig.lastUpdateId))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var updateResp struct {
		Ok          bool       `json:"ok"`
		Result      []TgUpdate `json:"result"`
		ErrorCode   int        `json:"error_code"`
		Description string     `json:"description"`
	}

	err = json.Unmarshal(body, &updateResp)
	if err != nil {
		return nil, fmt.Errorf("解析Telegram响应失败: %w. 响应内容: %s", err, string(body))
	}

	if !updateResp.Ok {
		return nil, fmt.Errorf("Telegram API返回错误 (代码: %d): %s", updateResp.ErrorCode, updateResp.Description)
	}

	return updateResp.Result, nil
}

func handleUpdate(update TgUpdate) {
	var incomingChatId int64
	configChatId, _ := strconv.ParseInt(appConfig.chat_id, 10, 64)

	if update.Message != nil {
		incomingChatId = update.Message.Chat.Id
	} else if update.CallbackQuery != nil {
		incomingChatId = update.CallbackQuery.Message.Chat.Id
	}

	if configChatId != 0 && incomingChatId != configChatId {
		fmt.Printf("忽略了来自未知 Chat ID (%d) 的消息\n", incomingChatId)
		return
	}

	if update.CallbackQuery != nil {
		go handleCallbackQuery(update.CallbackQuery)
	} else if update.Message != nil && update.Message.Text != "" {
		go handleMessage(update.Message)
	}
}

func handleMessage(msg *TgMessage) {
	chatId := msg.Chat.Id
	chatIdStr := strconv.FormatInt(chatId, 10)
	fmt.Printf("收到消息: %s 来自 Chat ID: %d\n", msg.Text, chatId)

	// 检查是否有待处理的用户输入
	mu.RLock()
	action, ok := userNextAction[chatId]
	mu.RUnlock()
	if ok {
		handleUserInput(msg, action)
		return
	}

	command := msg.Text
	if strings.HasPrefix(command, "/") {
		parts := strings.Split(command, " ")
		command = parts[0]
	}

	switch command {
	case "/start", "/menu":
		sendMessage(chatIdStr, "", "欢迎使用OCI助手机器人! 请选择一个操作:", buildMainMenuKeyboard())
	case "/list_tasks":
		sendTaskListKeyboard(chatIdStr, 0)
	default:
		sendMessage(chatIdStr, "", "未知命令。使用 /menu 查看主菜单。", nil)
	}
}

func handleCallbackQuery(cb *CallbackQuery) {
	answerCallbackQuery(cb.Id)

	chatId := cb.Message.Chat.Id
	chatIdStr := strconv.FormatInt(chatId, 10)
	messageId := cb.Message.MessageId
	parts := strings.Split(cb.Data, ":")
	action := parts[0]

	switch action {
	case "main_menu":
		sendMainMenuKeyboard(chatIdStr, messageId)
	case "select_tenant":
		sendTenantSelectionKeyboard(chatIdStr, "tenant_selected", messageId)
	case "tenant_selected":
		tenantName := parts[1]
		mu.Lock()
		selectedTenants[chatId] = tenantName
		mu.Unlock()
		sendTenantMenuKeyboard(chatIdStr, tenantName, messageId)
	case "tenant_menu":
		mu.RLock()
		tenantName, ok := selectedTenants[chatId]
		mu.RUnlock()
		if !ok {
			editMessage(messageId, chatIdStr, "", "错误: 未选择租户。请先返回主菜单选择。", buildMainMenuKeyboard())
			return
		}
		sendTenantMenuKeyboard(chatIdStr, tenantName, messageId)
	case "create_instance_menu":
		sendInstanceSelectionKeyboard(chatIdStr, messageId)
	case "list_instances_menu":
		go sendInstanceList(chatIdStr, messageId) // 使用 goroutine 避免阻塞
	case "create_instance":
		mu.RLock()
		tenantName, ok := selectedTenants[chatId]
		mu.RUnlock()
		if !ok {
			editMessage(messageId, chatIdStr, "", "错误: 会话已过期，请重新选择租户。", buildMainMenuKeyboard())
			return
		}
		instanceTemplate := parts[1]
		go startCreationTask(chatIdStr, tenantName, instanceTemplate)
		text := fmt.Sprintf("✅ 任务已创建!\n租户: *%s*\n模版: *%s*\n\n使用 /list_tasks 查看进度或停止任务。", tenantName, instanceTemplate)
		editMessage(messageId, chatIdStr, "", text, buildMainMenuKeyboard())
	case "list_tasks":
		sendTaskListKeyboard(chatIdStr, messageId)
	case "stop_task":
		taskID := parts[1]
		task, ok := taskManager.Get(taskID)
		if ok {
			task.cancelFunc()
			task.UpdateStatus("正在停止...")
		}
		sendTaskListKeyboard(chatIdStr, messageId)
	case "check_tenants":
		go func() {
			editMessage(messageId, chatIdStr, "", "正在检查所有租户凭证状态，请稍候...", nil)
			app := &App{}
			app.loadOracleSections(cfg)
			resultText := app.checkAllTenantsActivity(true)
			editMessage(messageId, chatIdStr, "所有租户凭证检查完成:", resultText, buildMainMenuKeyboard())
		}()
	case "instance_details":
		// --- 修改开始 ---
		instanceIndexStr := parts[1]
		instanceIndex, err := strconv.Atoi(instanceIndexStr)
		if err != nil {
			editMessage(messageId, chatIdStr, "", "错误: 无效的实例引用。", buildMainMenuKeyboard())
			return
		}

		instanceCacheMutex.RLock()
		cachedInstances, found := instanceListCache[chatId]
		instanceCacheMutex.RUnlock()

		if !found || instanceIndex >= len(cachedInstances) {
			editMessage(messageId, chatIdStr, "", "错误: 实例列表已过期，请刷新。", buildMainMenuKeyboard())
			return
		}

		instanceId := *cachedInstances[instanceIndex].Id
		sendInstanceDetailsKeyboard(chatIdStr, messageId, instanceId)
		// --- 修改结束 ---
	case "instance_action":
		actionType := parts[1]
		instanceId := parts[2]
		handleInstanceAction(chatIdStr, messageId, instanceId, actionType)
	case "change_ip":
		instanceId := parts[1]
		handleChangeIp(chatIdStr, messageId, instanceId)
	case "resize_disk_prompt":
		instanceId := parts[1]
		mu.Lock()
		userNextAction[chatId] = "enter_disk_size:" + instanceId
		mu.Unlock()
		editMessage(messageId, chatIdStr, "", "请输入新的引导卷大小 (GB)，例如: 100", nil)
	case "change_shape_prompt":
		instanceId := parts[1]
		mu.Lock()
		userNextAction[chatId] = "enter_shape_ocpu:" + instanceId
		mu.Unlock()
		editMessage(messageId, chatIdStr, "", "请输入新的OCPU数量 (例如: 4)", nil)
	}
}

// handleUserInput handles text input when the bot is expecting a specific response.
func handleUserInput(msg *TgMessage, action string) {
	chatId := msg.Chat.Id
	chatIdStr := strconv.FormatInt(chatId, 10)
	parts := strings.Split(action, ":")
	actionType := parts[0]

	// Clean up the pending action for this user
	mu.Lock()
	delete(userNextAction, chatId)
	mu.Unlock()

	switch actionType {
	case "enter_disk_size":
		instanceId := parts[1]
		size, err := strconv.ParseInt(msg.Text, 10, 64)
		if err != nil || size <= 0 {
			sendMessage(chatIdStr, "", "无效的大小。请输入一个正整数。", nil)
			return
		}

		go func() {
			sendMessage(chatIdStr, "", fmt.Sprintf("正在将实例引导卷大小调整为 *%d GB*...", size), nil)
			// Logic to resize disk
			mu.RLock()
			tenantName, ok := selectedTenants[chatId]
			mu.RUnlock()
			if !ok {
				sendMessage(chatIdStr, "", "错误: 会话已过期，请重新选择租户。", buildMainMenuKeyboard())
				return
			}
			app := &App{}
			app.loadOracleSections(cfg)
			var targetSection *ini.Section
			for _, sec := range app.oracleSections {
				if sec.Name() == tenantName {
					targetSection = sec
					break
				}
			}
			if err := app.initializeClients(targetSection); err != nil {
				sendMessage(chatIdStr, "", "错误: 初始化客户端失败: "+err.Error(), nil)
				return
			}

			// Find boot volume
			instance, err := getInstance(app.clients.Compute, &instanceId)
			if err != nil {
				sendMessage(chatIdStr, "", "错误: 获取实例信息失败: "+err.Error(), nil)
				return
			}

			attachments, err := listBootVolumeAttachments(app.clients.Compute, instance.AvailabilityDomain, instance.CompartmentId, nil)
			if err != nil {
				sendMessage(chatIdStr, "", "错误: 获取引导卷附件失败: "+err.Error(), nil)
				return
			}
			var bootVolumeId *string
			for _, att := range attachments {
				if *att.InstanceId == instanceId {
					bootVolumeId = att.BootVolumeId
					break
				}
			}

			if bootVolumeId == nil {
				sendMessage(chatIdStr, "", "错误: 未找到实例的引导卷。", nil)
				return
			}

			_, err = updateBootVolume(app.clients.Storage, bootVolumeId, &size, nil)
			if err != nil {
				sendMessage(chatIdStr, "", "错误: 修改引导卷大小失败: "+err.Error(), nil)
			} else {
				sendMessage(chatIdStr, "", "✅ 引导卷大小修改成功！实例将重启以应用更改。", nil)
				// Reboot instance to apply change
				instanceAction(app.clients.Compute, &instanceId, core.InstanceActionActionSoftreset)
			}
		}()

	case "enter_shape_ocpu":
		instanceId := parts[1]
		ocpu, err := strconv.ParseFloat(msg.Text, 32)
		if err != nil || ocpu <= 0 {
			sendMessage(chatIdStr, "", "无效的OCPU数量。请输入一个正数。", nil)
			return
		}
		mu.Lock()
		// Ask for memory, passing along instanceId and ocpu
		userNextAction[chatId] = fmt.Sprintf("enter_shape_memory:%s:%f", instanceId, ocpu)
		mu.Unlock()
		sendMessage(chatIdStr, "", "请输入新的内存大小 (GB)，例如: 24", nil)

	case "enter_shape_memory":
		instanceId := parts[1]
		ocpuStr := parts[2]
		ocpu64, _ := strconv.ParseFloat(ocpuStr, 32)
		ocpu := float32(ocpu64)

		memory, err := strconv.ParseFloat(msg.Text, 32)
		if err != nil || memory <= 0 {
			sendMessage(chatIdStr, "", "无效的内存大小。请输入一个正数。", nil)
			return
		}
		memory32 := float32(memory)

		go func() {
			sendMessage(chatIdStr, "", fmt.Sprintf("正在将实例规格更改为 *%g OCPU* 和 *%g GB* 内存...", ocpu, memory32), nil)
			// Logic to change shape
			mu.RLock()
			tenantName, ok := selectedTenants[chatId]
			mu.RUnlock()
			if !ok {
				sendMessage(chatIdStr, "", "错误: 会话已过期，请重新选择租户。", buildMainMenuKeyboard())
				return
			}
			app := &App{}
			app.loadOracleSections(cfg)
			var targetSection *ini.Section
			for _, sec := range app.oracleSections {
				if sec.Name() == tenantName {
					targetSection = sec
					break
				}
			}
			if err := app.initializeClients(targetSection); err != nil {
				sendMessage(chatIdStr, "", "错误: 初始化客户端失败: "+err.Error(), nil)
				return
			}
			_, err := updateInstance(app.clients.Compute, &instanceId, nil, &ocpu, &memory32, nil, nil)
			if err != nil {
				sendMessage(chatIdStr, "", "错误: 升级/降级实例失败: "+err.Error(), nil)
			} else {
				sendMessage(chatIdStr, "", "✅ 实例规格修改成功！", nil)
			}
		}()
	}
}

// sendInstanceDetailsKeyboard displays details for a specific instance with action buttons.
func sendInstanceDetailsKeyboard(chatId string, messageId int, instanceId string) {
	chatIdInt, _ := strconv.ParseInt(chatId, 10, 64)
	mu.RLock()
	tenantName, ok := selectedTenants[chatIdInt]
	mu.RUnlock()

	if !ok {
		editMessage(messageId, chatId, "", "错误: 会话已过期，请重新选择租户。", buildMainMenuKeyboard())
		return
	}

	editMessage(messageId, chatId, "", fmt.Sprintf("正在为租户 *%s* 获取实例 *%s* 的详细信息...", tenantName, instanceId[:8]), nil)

	app := &App{}
	app.loadOracleSections(cfg)
	var targetSection *ini.Section
	for _, sec := range app.oracleSections {
		if sec.Name() == tenantName {
			targetSection = sec
			break
		}
	}
	if targetSection == nil {
		editMessage(messageId, chatId, "", "错误: 未找到租户。", buildMainMenuKeyboard())
		return
	}
	if err := app.initializeClients(targetSection); err != nil {
		editMessage(messageId, chatId, "", "错误: 初始化客户端失败: "+err.Error(), nil)
		return
	}

	instance, err := getInstance(app.clients.Compute, &instanceId)
	if err != nil {
		editMessage(messageId, chatId, "", "错误: 获取实例信息失败: "+err.Error(), nil)
		return
	}

	ips, _ := getInstancePublicIps(app.clients, &instanceId)
	ipStr := strings.Join(ips, ", ")
	if ipStr == "" {
		ipStr = "N/A"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("实例: *%s*\n", *instance.DisplayName))
	sb.WriteString(fmt.Sprintf("状态: *%s*\n", getInstanceState(instance.LifecycleState)))
	sb.WriteString(fmt.Sprintf("IP: `%s`\n", ipStr))
	sb.WriteString(fmt.Sprintf("配置: *%s*\n", *instance.Shape))
	sb.WriteString(fmt.Sprintf("  - OCPU: *%g*\n", *instance.ShapeConfig.Ocpus))
	sb.WriteString(fmt.Sprintf("  - 内存: *%g GB*\n", *instance.ShapeConfig.MemoryInGBs))
	sb.WriteString(fmt.Sprintf("可用区: *%s*\n", *instance.AvailabilityDomain))
	sb.WriteString(fmt.Sprintf("创建时间: *%s*", instance.TimeCreated.Format(timeLayout)))

	state := instance.LifecycleState
	var actionButtons [][]InlineKeyboardButton
	if state == core.InstanceLifecycleStateStopped {
		actionButtons = append(actionButtons, []InlineKeyboardButton{
			{Text: "▶️ 启动", CallbackData: "instance_action:start:" + instanceId},
		})
	} else if state == core.InstanceLifecycleStateRunning {
		actionButtons = append(actionButtons, []InlineKeyboardButton{
			{Text: "⏹️ 停止", CallbackData: "instance_action:stop:" + instanceId},
			{Text: "🔄 重启", CallbackData: "instance_action:reboot:" + instanceId},
		})
	}

	actionButtons = append(actionButtons, []InlineKeyboardButton{
		{Text: "💣 终止", CallbackData: "instance_action:terminate:" + instanceId},
		{Text: " IP 更换", CallbackData: "change_ip:" + instanceId},
	})

	var flexButtons [][]InlineKeyboardButton
	if strings.Contains(strings.ToLower(*instance.Shape), "flex") {
		flexButtons = append(flexButtons, []InlineKeyboardButton{
			{Text: "💪 修改配置", CallbackData: "change_shape_prompt:" + instanceId},
		})
	}
	flexButtons = append(flexButtons, []InlineKeyboardButton{
		{Text: "💾 修改磁盘", CallbackData: "resize_disk_prompt:" + instanceId},
	})

	var allButtonRows [][]InlineKeyboardButton
	allButtonRows = append(allButtonRows, actionButtons...)
	allButtonRows = append(allButtonRows, flexButtons...)
	allButtonRows = append(allButtonRows, []InlineKeyboardButton{
		{Text: "🔄 刷新", CallbackData: "instance_details:" + instanceId},
	})
	allButtonRows = append(allButtonRows, []InlineKeyboardButton{
		{Text: "« 返回实例列表", CallbackData: "list_instances_menu"},
	})

	keyboard := &InlineKeyboardMarkup{
		InlineKeyboard: allButtonRows,
	}

	editMessage(messageId, chatId, "", sb.String(), keyboard)
}

// handleInstanceAction performs an action (start, stop, etc.) on an instance.
func handleInstanceAction(chatId string, messageId int, instanceId string, actionType string) {
	chatIdInt, _ := strconv.ParseInt(chatId, 10, 64)
	mu.RLock()
	tenantName, ok := selectedTenants[chatIdInt]
	mu.RUnlock()

	if !ok {
		editMessage(messageId, chatId, "", "错误: 会话已过期，请重新选择租户。", buildMainMenuKeyboard())
		return
	}

	editMessage(messageId, chatId, "", fmt.Sprintf("正在对实例 *%s* 执行操作: *%s*...", instanceId[:8], actionType), nil)

	app := &App{}
	app.loadOracleSections(cfg)
	var targetSection *ini.Section
	for _, sec := range app.oracleSections {
		if sec.Name() == tenantName {
			targetSection = sec
			break
		}
	}
	if err := app.initializeClients(targetSection); err != nil {
		editMessage(messageId, chatId, "", "错误: 初始化客户端失败: "+err.Error(), nil)
		return
	}

	var err error
	var actionEnum core.InstanceActionActionEnum
	switch actionType {
	case "start":
		actionEnum = core.InstanceActionActionStart
	case "stop":
		actionEnum = core.InstanceActionActionSoftstop
	case "reboot":
		actionEnum = core.InstanceActionActionSoftreset
	case "terminate":
		// Special handling for terminate
		go func() {
			err := terminateInstance(app.clients.Compute, &instanceId)
			if err != nil {
				sendMessage(chatId, "", "❌ 终止实例失败: "+err.Error(), nil)
			} else {
				sendMessage(chatId, "", "✅ 实例已成功终止。", nil)
				// Go back to list view after termination
				sendInstanceList(chatId, messageId)
			}
		}()
		return // Return early as terminate is async and we don't refresh the same view
	default:
		editMessage(messageId, chatId, "", "未知操作。", nil)
		return
	}

	_, err = instanceAction(app.clients.Compute, &instanceId, actionEnum)
	if err != nil {
		editMessage(messageId, chatId, "", fmt.Sprintf("❌ 操作 '%s' 失败: %s", actionType, err.Error()), nil)
	} else {
		editMessage(messageId, chatId, "", fmt.Sprintf("✅ 操作 '%s' 请求已发送。请稍后刷新查看状态。", actionType), nil)
	}

	// Refresh details after a short delay
	time.Sleep(3 * time.Second)
	sendInstanceDetailsKeyboard(chatId, messageId, instanceId)
}

// handleChangeIp handles changing the public IP of an instance.
func handleChangeIp(chatId string, messageId int, instanceId string) {
	chatIdInt, _ := strconv.ParseInt(chatId, 10, 64)
	mu.RLock()
	tenantName, ok := selectedTenants[chatIdInt]
	mu.RUnlock()

	if !ok {
		editMessage(messageId, chatId, "", "错误: 会话已过期，请重新选择租户。", buildMainMenuKeyboard())
		return
	}

	editMessage(messageId, chatId, "", fmt.Sprintf("正在为实例 *%s* 更换IP...", instanceId[:8]), nil)

	app := &App{}
	app.loadOracleSections(cfg)
	var targetSection *ini.Section
	for _, sec := range app.oracleSections {
		if sec.Name() == tenantName {
			targetSection = sec
			break
		}
	}
	if err := app.initializeClients(targetSection); err != nil {
		editMessage(messageId, chatId, "", "错误: 初始化客户端失败: "+err.Error(), nil)
		return
	}

	vnics, err := getInstanceVnics(app.clients, &instanceId)
	if err != nil || len(vnics) == 0 {
		editMessage(messageId, chatId, "", "错误: 获取实例网络信息失败: "+err.Error(), nil)
		return
	}

	publicIp, err := changePublicIp(app.clients, vnics)
	if err != nil {
		editMessage(messageId, chatId, "", "❌ 更换IP失败: "+err.Error(), nil)
	} else {
		editMessage(messageId, chatId, "", fmt.Sprintf("✅ 更换IP成功！新IP: `%s`", *publicIp.IpAddress), nil)
	}

	// Refresh details after a short delay
	time.Sleep(3 * time.Second)
	sendInstanceDetailsKeyboard(chatId, messageId, instanceId)
}

func sendMainMenuKeyboard(chatId string, messageId int) {
	text := "欢迎使用OCI助手机器人! 请选择一个操作:"
	if messageId > 0 {
		editMessage(messageId, chatId, "", text, buildMainMenuKeyboard())
	} else {
		sendMessage(chatId, "", text, buildMainMenuKeyboard())
	}
}

func buildMainMenuKeyboard() *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{
			{{Text: "👤 选择租户", CallbackData: "select_tenant"}},
			{{Text: "📊 查看抢机任务", CallbackData: "list_tasks"}},
			{{Text: "🔑 检查所有租户", CallbackData: "check_tenants"}},
		},
	}
}

func sendTenantMenuKeyboard(chatId, tenantName string, messageId int) {
	text := fmt.Sprintf("当前租户: *%s*\n请选择一个操作:", tenantName)
	keyboard := &InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{
			{{Text: "⚙️ 创建实例", CallbackData: "create_instance_menu"}},
			{{Text: "🖥️ 查看实例列表", CallbackData: "list_instances_menu"}},
			{{Text: "« 返回主菜单", CallbackData: "main_menu"}},
		},
	}
	editMessage(messageId, chatId, "", text, keyboard)
}

func sendTenantSelectionKeyboard(chatId, callbackPrefix string, messageId int) {
	app := &App{}
	app.loadOracleSections(cfg)
	var buttons [][]InlineKeyboardButton
	for _, section := range app.oracleSections {
		row := []InlineKeyboardButton{
			{Text: section.Name(), CallbackData: callbackPrefix + ":" + section.Name()},
		}
		buttons = append(buttons, row)
	}
	buttons = append(buttons, []InlineKeyboardButton{{Text: "« 返回主菜单", CallbackData: "main_menu"}})
	keyboard := InlineKeyboardMarkup{InlineKeyboard: buttons}
	editMessage(messageId, chatId, "请选择一个租户:", "", &keyboard)
}

func sendInstanceSelectionKeyboard(chatId string, messageId int) {
	chatIdInt, _ := strconv.ParseInt(chatId, 10, 64)
	mu.RLock()
	tenantName, ok := selectedTenants[chatIdInt]
	mu.RUnlock()

	if !ok {
		editMessage(messageId, chatId, "", "错误: 未选择租户。请先返回主菜单选择。", buildMainMenuKeyboard())
		return
	}

	app := &App{}
	app.loadOracleSections(cfg)
	var targetSection *ini.Section
	for _, sec := range app.oracleSections {
		if sec.Name() == tenantName {
			targetSection = sec
			break
		}
	}
	if targetSection == nil {
		editMessage(messageId, chatId, "", "错误: 未找到租户配置。", buildMainMenuKeyboard())
		return
	}

	var instanceSections []*ini.Section
	instanceSections = append(instanceSections, app.instanceBaseSection.ChildSections()...)
	instanceSections = append(instanceSections, targetSection.ChildSections()...)
	if len(instanceSections) == 0 {
		editMessage(messageId, chatId, "", "此租户下未找到任何实例模版。", buildMainMenuKeyboard())
		return
	}

	var buttons [][]InlineKeyboardButton
	for _, sec := range instanceSections {
		shape := sec.Key("shape").Value()
		row := []InlineKeyboardButton{
			{Text: fmt.Sprintf("%s (%s)", sec.Name(), shape), CallbackData: "create_instance:" + sec.Name()},
		}
		buttons = append(buttons, row)
	}
	buttons = append(buttons, []InlineKeyboardButton{{Text: "« 返回租户菜单", CallbackData: "tenant_menu"}})
	keyboard := InlineKeyboardMarkup{InlineKeyboard: buttons}
	editMessage(messageId, chatId, "", fmt.Sprintf("当前租户: *%s*\n请选择要创建的实例模版:", tenantName), &keyboard)
}

func sendTaskListKeyboard(chatId string, messageId int) {
	tasks := taskManager.List()
	var sb strings.Builder
	var keyboard InlineKeyboardMarkup

	if len(tasks) == 0 {
		sb.WriteString("当前没有正在运行的抢机任务。")
		keyboard = InlineKeyboardMarkup{
			InlineKeyboard: [][]InlineKeyboardButton{
				{{Text: "« 返回主菜单", CallbackData: "main_menu"}},
			},
		}
	} else {
		sb.WriteString("当前抢机任务列表:\n\n")
		var buttons [][]InlineKeyboardButton
		for _, task := range tasks {
			sb.WriteString(task.GetStatus() + "\n\n")
			buttons = append(buttons, []InlineKeyboardButton{
				{Text: "❌ 停止任务 " + task.ID[:8], CallbackData: "stop_task:" + task.ID},
			})
		}
		buttons = append(buttons, []InlineKeyboardButton{{Text: "🔄 刷新", CallbackData: "list_tasks"}})
		buttons = append(buttons, []InlineKeyboardButton{{Text: "« 返回主菜单", CallbackData: "main_menu"}})
		keyboard.InlineKeyboard = buttons
	}

	if messageId > 0 {
		editMessage(messageId, chatId, "", sb.String(), &keyboard)
	} else {
		sendMessage(chatId, "", sb.String(), &keyboard)
	}
}

// escapeLegacyMarkdown is a helper function to escape characters for Telegram's legacy Markdown parse mode.
func escapeLegacyMarkdown(s string) string {
	s = strings.ReplaceAll(s, "_", "\\_")
	s = strings.ReplaceAll(s, "*", "\\*")
	s = strings.ReplaceAll(s, "`", "\\`")
	s = strings.ReplaceAll(s, "[", "\\[")
	return s
}

func sendInstanceList(chatId string, messageId int) {
	chatIdInt, _ := strconv.ParseInt(chatId, 10, 64)
	mu.RLock()
	tenantName, ok := selectedTenants[chatIdInt]
	mu.RUnlock()

	if !ok {
		editMessage(messageId, chatId, "", "错误: 未选择租户。请先返回主菜单选择。", buildMainMenuKeyboard())
		return
	}

	editMessage(messageId, chatId, "", fmt.Sprintf("正在连接租户 *%s*...", escapeLegacyMarkdown(tenantName)), nil)
	fmt.Printf("Bot: 正在连接租户 %s...\n", tenantName)

	app := &App{}
	app.loadOracleSections(cfg)
	var targetSection *ini.Section
	for _, sec := range app.oracleSections {
		if sec.Name() == tenantName {
			targetSection = sec
			break
		}
	}
	if targetSection == nil {
		editMessage(messageId, chatId, "", "错误: 在配置文件中未找到租户。", buildMainMenuKeyboard())
		return
	}

	if err := app.initializeClients(targetSection); err != nil {
		errorMsg := fmt.Sprintf("❌ 连接租户失败: %v", err)
		editMessage(messageId, chatId, "", escapeLegacyMarkdown(errorMsg), nil)
		fmt.Printf("Bot: 连接租户 %s 失败: %v\n", tenantName, err)
		return
	}
	fmt.Printf("Bot: 租户 %s 连接成功。\n", tenantName)

	editMessage(messageId, chatId, "", fmt.Sprintf("✅ 租户 *%s* 连接成功!\n正在获取实例列表...", escapeLegacyMarkdown(tenantName)), nil)
	fmt.Printf("Bot: 正在为租户 %s 获取实例列表...\n", tenantName)

	var allInstances []core.Instance
	var instances []core.Instance
	var nextPage *string
	var err error
	for {
		instances, nextPage, err = ListInstances(ctx, app.clients.Compute, &app.oracleConfig.Tenancy, nextPage)
		if err == nil {
			allInstances = append(allInstances, instances...)
		}
		if nextPage == nil || len(instances) == 0 {
			break
		}
	}

	if err != nil {
		errorMsg := fmt.Sprintf("❌ 获取实例列表失败: %v", err)
		editMessage(messageId, chatId, "", escapeLegacyMarkdown(errorMsg), nil)
		fmt.Printf("Bot: 获取实例列表失败: %v\n", err)
		return
	}
	fmt.Printf("Bot: 成功获取 %d 个实例。\n", len(allInstances))

	// --- 修改开始 ---
	// 将获取到的实例列表存入缓存
	instanceCacheMutex.Lock()
	instanceListCache[chatIdInt] = allInstances
	instanceCacheMutex.Unlock()
	// --- 修改结束 ---

	var sb strings.Builder
	escapedTenantName := escapeLegacyMarkdown(tenantName)
	sb.WriteString(fmt.Sprintf("租户 *%s* 的实例列表:\n", escapedTenantName))
	var buttons [][]InlineKeyboardButton

	if len(allInstances) == 0 {
		sb.WriteString("没有找到任何实例。")
	} else {
		for i, inst := range allInstances { // 注意这里使用了索引 i
			displayName := escapeLegacyMarkdown(*inst.DisplayName)
			shape := escapeLegacyMarkdown(*inst.Shape)
			state := getInstanceState(inst.LifecycleState)
			sb.WriteString(fmt.Sprintf("\n- *%s* (%s): %s\n", displayName, shape, state))

			// --- 修改开始 ---
			// 使用索引作为 callback_data
			buttons = append(buttons, []InlineKeyboardButton{{
				Text:         fmt.Sprintf("管理 %s", *inst.DisplayName),
				CallbackData: "instance_details:" + strconv.Itoa(i), // 使用索引
			}})
			// --- 修改结束 ---
		}
	}

	buttons = append(buttons, []InlineKeyboardButton{{Text: "🔄 刷新", CallbackData: "list_instances_menu"}})
	buttons = append(buttons, []InlineKeyboardButton{{Text: "« 返回租户菜单", CallbackData: "tenant_menu"}})
	keyboard := &InlineKeyboardMarkup{InlineKeyboard: buttons}

	_, err = editMessage(messageId, chatId, "", sb.String(), keyboard)
	if err != nil {
		fmt.Printf("Bot: 发送最终实例列表失败: %v\n", err)
		fallbackText := fmt.Sprintf("❌ 加载实例列表时出错: %v", err)
		sendMessage(chatId, "", escapeLegacyMarkdown(fallbackText), buildMainMenuKeyboard())
	} else {
		fmt.Println("Bot: 成功发送实例列表。")
	}
}

func startCreationTask(chatId, tenantName, instanceTemplate string) {
	ctx, cancel := context.WithCancel(context.Background())
	task := &CreationTask{
		ID:               uuid.New().String(),
		TenantName:       tenantName,
		InstanceTemplate: instanceTemplate,
		Status:           "初始化中",
		StartTime:        time.Now(),
		cancelFunc:       cancel,
	}
	taskManager.Add(task)
	defer taskManager.Remove(task.ID)

	app := &App{}
	app.loadOracleSections(cfg)
	var targetSection *ini.Section
	for _, sec := range app.oracleSections {
		if sec.Name() == tenantName {
			targetSection = sec
			break
		}
	}
	if targetSection == nil {
		task.UpdateStatus("失败: 未找到租户")
		return
	}
	if err := app.initializeClients(targetSection); err != nil {
		task.UpdateStatus("失败: 初始化客户端失败")
		return
	}
	var instanceSections []*ini.Section
	instanceSections = append(instanceSections, app.instanceBaseSection.ChildSections()...)
	instanceSections = append(instanceSections, targetSection.ChildSections()...)
	var targetInstanceSection *ini.Section
	for _, sec := range instanceSections {
		if sec.Name() == instanceTemplate {
			targetInstanceSection = sec
			break
		}
	}
	if targetInstanceSection == nil {
		task.UpdateStatus("失败: 未找到实例模版")
		return
	}
	instance := Instance{}
	if err := targetInstanceSection.MapTo(&instance); err != nil {
		task.UpdateStatus("失败: 解析实例模版失败")
		return
	}

	appConfig.chat_id = chatId
	appConfig.each = true
	task.TotalCount = instance.Sum
	task.UpdateStatus("运行中")
	app.LaunchInstances(ctx, task, app.availabilityDomains, instance)

	finalStatus := task.Status
	if finalStatus == "运行中" {
		finalStatus = "完成"
	}
	task.UpdateStatus(finalStatus)
	sendMessage(chatId, "", "任务 "+task.ID[:8]+" 已结束，状态: "+finalStatus, nil)
}

func answerCallbackQuery(callbackQueryId string) {
	client := getTgClient()
	apiURL := "https://api.telegram.org/bot" + appConfig.token + "/answerCallbackQuery"
	_, err := client.PostForm(apiURL, url.Values{"callback_query_id": {callbackQueryId}})
	if err != nil {
		printlnErr("回答Callback Query失败", err.Error())
	}
}

func (app *App) LaunchInstances(ctx context.Context, task *CreationTask, ads []identity.AvailabilityDomain, instance Instance) {
	var adCount int32 = int32(len(ads))
	adName := common.String(instance.AvailabilityDomain)
	each := instance.Each
	sum := instance.Sum
	if sum == 0 {
		sum = 1 // 默认创建1个实例
	}
	task.TotalCount = sum

	var usableAds = make([]identity.AvailabilityDomain, 0)
	var AD_NOT_FIXED bool = false
	var EACH_AD = false
	if adName == nil || *adName == "" {
		AD_NOT_FIXED = true
		if each > 0 {
			EACH_AD = true
			sum = each * adCount
			task.TotalCount = sum
		} else {
			EACH_AD = false
			usableAds = ads
		}
	}
	name := instance.InstanceDisplayName
	if name == "" {
		name = time.Now().Format("instance-20060102-1504")
	}
	displayName := common.String(name)
	if sum > 1 {
		displayName = common.String(name + "-1")
	}
	request := core.LaunchInstanceRequest{}
	request.CompartmentId = common.String(app.oracleConfig.Tenancy)
	request.DisplayName = displayName
	image, err := GetImage(context.Background(), app.clients.Compute, &app.oracleConfig.Tenancy, instance.OperatingSystem, instance.OperatingSystemVersion, instance.Shape)
	if err != nil {
		printlnErr("获取系统镜像失败", err.Error())
		task.UpdateStatus("失败: " + err.Error())
		return
	}
	var shape core.Shape
	if strings.Contains(strings.ToLower(instance.Shape), "flex") && instance.Ocpus > 0 && instance.MemoryInGBs > 0 {
		shape.Shape = &instance.Shape
		shape.Ocpus = &instance.Ocpus
		shape.MemoryInGBs = &instance.MemoryInGBs
	} else {
		shape, err = getShape(app.clients.Compute, image.Id, instance.Shape, &app.oracleConfig.Tenancy)
		if err != nil {
			printlnErr("获取Shape信息失败", err.Error())
			task.UpdateStatus("失败: " + err.Error())
			return
		}
	}
	request.Shape = shape.Shape
	if strings.Contains(strings.ToLower(*shape.Shape), "flex") {
		request.ShapeConfig = &core.LaunchInstanceShapeConfigDetails{
			Ocpus:       shape.Ocpus,
			MemoryInGBs: shape.MemoryInGBs,
		}
		if instance.Burstable == "1/8" {
			request.ShapeConfig.BaselineOcpuUtilization = core.LaunchInstanceShapeConfigDetailsBaselineOcpuUtilization8
		} else if instance.Burstable == "1/2" {
			request.ShapeConfig.BaselineOcpuUtilization = core.LaunchInstanceShapeConfigDetailsBaselineOcpuUtilization2
		}
	}
	subnet, err := CreateOrGetNetworkInfrastructure(context.Background(), app.clients.Network, &app.oracleConfig.Tenancy, instance)
	if err != nil {
		printlnErr("获取子网失败", err.Error())
		task.UpdateStatus("失败: " + err.Error())
		return
	}
	request.CreateVnicDetails = &core.CreateVnicDetails{SubnetId: subnet.Id}
	sd := core.InstanceSourceViaImageDetails{}
	sd.ImageId = image.Id
	if instance.BootVolumeSizeInGBs > 0 {
		sd.BootVolumeSizeInGBs = common.Int64(instance.BootVolumeSizeInGBs)
	}
	request.SourceDetails = sd
	request.IsPvEncryptionInTransitEnabled = common.Bool(true)
	metaData := map[string]string{}
	metaData["ssh_authorized_keys"] = instance.SSH_Public_Key
	if instance.CloudInit != "" {
		encodedString := base64.StdEncoding.EncodeToString([]byte(instance.CloudInit))
		metaData["user_data"] = encodedString
	}
	request.Metadata = metaData
	minTime := instance.MinTime
	maxTime := instance.MaxTime
	SKIP_RETRY_MAP := make(map[int32]bool)
	var failTimes int32 = 0
	var adIndex int32 = 0
	var pos int32 = 0
	var SUCCESS = false

	for pos < sum {
		select {
		case <-ctx.Done():
			task.UpdateStatus("用户已手动停止")
			return
		default:
		}
		task.mu.Lock()
		task.Attempts++
		task.mu.Unlock()

		if AD_NOT_FIXED {
			if EACH_AD {
				if pos%each == 0 && failTimes == 0 {
					adName = ads[adIndex].Name
					adIndex++
				}
			} else {
				if SUCCESS {
					adIndex = 0
				}
				if adIndex >= adCount {
					adIndex = 0
				}
				adName = usableAds[adIndex].Name
				adIndex++
			}
		}
		request.AvailabilityDomain = adName
		createResp, err := app.clients.Compute.LaunchInstance(context.Background(), request)
		if err == nil {
			SUCCESS = true
			task.mu.Lock()
			task.SuccessCount++
			task.mu.Unlock()

			ips, errIp := getInstancePublicIps(app.clients, createResp.Instance.Id)
			var text string
			if errIp != nil {
				text = fmt.Sprintf("✅ 第 %d/%d 个实例抢到了，但启动失败: %s", task.SuccessCount, task.TotalCount, errIp.Error())
			} else {
				text = fmt.Sprintf("✅ 第 %d/%d 个实例抢到了! IP: %s", task.SuccessCount, task.TotalCount, strings.Join(ips, ","))
			}
			sendMessage(appConfig.chat_id, fmt.Sprintf("[%s]", app.oracleSectionName), text, nil)
			pos++
			displayName = common.String(fmt.Sprintf("%s-%d", name, pos+1))
			request.DisplayName = displayName
		} else {
			SUCCESS = false
			errInfo := err.Error()
			servErr, isServErr := common.IsServiceError(err)
			if isServErr {
				errInfo = servErr.GetMessage()
				task.mu.Lock()
				if time.Since(task.LastMessageTimestamp) > 30*time.Second && task.LastMessage != errInfo {
					task.LastMessage = errInfo
					task.LastMessageTimestamp = time.Now()
					sendMessage(appConfig.chat_id, fmt.Sprintf("[%s]", app.oracleSectionName), "❌ 抢机失败: "+errInfo, nil)
				}
				task.mu.Unlock()
			}
			if isServErr && (400 <= servErr.GetHTTPStatusCode() && servErr.GetHTTPStatusCode() <= 405) ||
				(servErr.GetHTTPStatusCode() == 409 && !strings.EqualFold(servErr.GetCode(), "IncorrectState")) ||
				servErr.GetHTTPStatusCode() == 412 || servErr.GetHTTPStatusCode() == 413 || servErr.GetHTTPStatusCode() == 422 ||
				servErr.GetHTTPStatusCode() == 431 || servErr.GetHTTPStatusCode() == 501 {
				if AD_NOT_FIXED && !EACH_AD {
					SKIP_RETRY_MAP[adIndex-1] = true
				}
			}
		}

		select {
		case <-ctx.Done():
			task.UpdateStatus("用户已手动停止")
			return
		case <-time.After(time.Duration(sleepRandomSecond(minTime, maxTime)) * time.Second):
			// 继续循环
		}
	}
}

func (app *App) selectOracleAccount() (*ini.Section, bool) {
	if len(app.oracleSections) == 1 {
		return app.oracleSections[0], false
	}

	fmt.Printf("\n\033[1;32m%s\033[0m\n\n", "欢迎使用甲骨文实例管理工具")
	w := new(tabwriter.Writer)
	w.Init(os.Stdout, 4, 8, 1, '\t', 0)
	fmt.Fprintf(w, "%s\t%s\t\n", "序号", "账号")
	for i, section := range app.oracleSections {
		fmt.Fprintf(w, "%d\t%s\t\n", i+1, section.Name())
	}
	w.Flush()
	fmt.Println()

	for {
		fmt.Print("请输入账号对应的序号进入相关操作 (或输入 'q' 退出): ")
		var input string
		_, err := fmt.Scanln(&input)
		if err != nil || strings.EqualFold(input, "q") {
			return nil, true
		}

		if strings.EqualFold(input, "oci") {
			app.multiBatchLaunchInstances()
			continue
		} else if strings.EqualFold(input, "ip") {
			app.multiBatchListInstancesIp()
			continue
		}

		index, _ := strconv.Atoi(input)
		if index > 0 && index <= len(app.oracleSections) {
			return app.oracleSections[index-1], false
		}
		fmt.Printf("\033[1;31m错误! 请输入正确的序号\033[0m\n")
	}
}
func (app *App) initializeClients(oracleSec *ini.Section) error {
	app.oracleSection = oracleSec
	app.oracleSectionName = oracleSec.Name()
	app.oracleConfig = Oracle{}
	err := oracleSec.MapTo(&app.oracleConfig)
	if err != nil {
		return fmt.Errorf("解析账号相关参数失败: %w", err)
	}

	provider, err := getProvider(app.oracleConfig)
	if err != nil {
		return fmt.Errorf("获取 Provider 失败: %w", err)
	}

	// 创建带超时的HTTP客户端
	httpClient := getOciHttpClient()

	clients := &OciClients{Provider: provider}
	clients.Compute, err = core.NewComputeClientWithConfigurationProvider(provider)
	if err != nil {
		return fmt.Errorf("创建 ComputeClient 失败: %w", err)
	}
	clients.Compute.HTTPClient = httpClient

	clients.Network, err = core.NewVirtualNetworkClientWithConfigurationProvider(provider)
	if err != nil {
		return fmt.Errorf("创建 VirtualNetworkClient 失败: %w", err)
	}
	clients.Network.HTTPClient = httpClient

	clients.Storage, err = core.NewBlockstorageClientWithConfigurationProvider(provider)
	if err != nil {
		return fmt.Errorf("创建 BlockstorageClient 失败: %w", err)
	}
	clients.Storage.HTTPClient = httpClient

	clients.Identity, err = identity.NewIdentityClientWithConfigurationProvider(provider)
	if err != nil {
		return fmt.Errorf("创建 IdentityClient 失败: %w", err)
	}
	clients.Identity.HTTPClient = httpClient

	clients.Monitoring, err = monitoring.NewMonitoringClientWithConfigurationProvider(provider)
	if err != nil {
		return fmt.Errorf("创建 MonitoringClient 失败: %w", err)
	}
	clients.Monitoring.HTTPClient = httpClient

	app.clients = clients

	fmt.Println("正在获取可用性域...")
	app.availabilityDomains, err = ListAvailabilityDomains(app.clients)
	if err != nil {
		return fmt.Errorf("获取可用性域失败: %w", err)
	}
	fmt.Println("获取可用性域成功。")

	return nil
}
func (app *App) showMainMenu() {
	for {
		fmt.Printf("\n\033[1;32m欢迎使用甲骨文实例管理工具\033[0m \n(当前账号: %s)\n\n", app.oracleSectionName)
		w := new(tabwriter.Writer)
		w.Init(os.Stdout, 0, 8, 2, '\t', 0)
		fmt.Fprintln(w, "1.\t查看实例")
		fmt.Fprintln(w, "2.\t创建实例")
		fmt.Fprintln(w, "3.\t管理引导卷")
		fmt.Fprintln(w, "4.\t网络管理")
		fmt.Fprintln(w, "5.\t管理员管理")
		fmt.Fprintln(w, "6.\t租户与用户信息")
		fmt.Fprintln(w, "7.\t租户管理 (凭证检查)")
		w.Flush()
		fmt.Print("\n请输入序号进入相关操作 (输入 'q' 或直接回车返回): ")
		var input string
		fmt.Scanln(&input)
		if strings.EqualFold(input, "q") || input == "" {
			return // 返回到账号选择
		}

		if strings.EqualFold(input, "oci") {
			app.batchLaunchInstances(app.oracleSection)
			continue
		} else if strings.EqualFold(input, "ip") {
			IPsFilePath := IPsFilePrefix + "-" + time.Now().Format("2006-01-02-150405.txt")
			app.batchListInstancesIp(IPsFilePath, app.oracleSection)
			continue
		}

		num, _ := strconv.Atoi(input)
		switch num {
		case 1:
			app.listInstances()
		case 2:
			app.listLaunchInstanceTemplates()
		case 3:
			app.listBootVolumes()
		case 4:
			app.manageNetwork()
		case 5:
			app.manageAdmins()
		case 6:
			app.manageTenantAndUser()
		case 7:
			app.manageTenants()
		default:
			fmt.Println("\033[1;31m无效的输入。\033[0m")
		}
	}
}
func (app *App) listInstances() {
	fmt.Println("正在获取实例数据...")
	var instances []core.Instance
	var ins []core.Instance
	var nextPage *string
	var err error
	for {
		ins, nextPage, err = ListInstances(ctx, app.clients.Compute, &app.oracleConfig.Tenancy, nextPage)
		if err == nil {
			instances = append(instances, ins...)
		}
		if nextPage == nil || len(ins) == 0 {
			break
		}
	}

	if err != nil {
		printlnErr("获取失败, 回车返回上一级菜单.", err.Error())
		fmt.Scanln()
		return
	}
	if len(instances) == 0 {
		fmt.Printf("\033[1;32m实例为空, 回车返回上一级菜单.\033[0m")
		fmt.Scanln()
		return
	}
	fmt.Printf("\n\033[1;32m实例信息\033[0m \n(当前账号: %s)\n\n", app.oracleSectionName)
	w := new(tabwriter.Writer)
	w.Init(os.Stdout, 4, 8, 1, '\t', 0)
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t\n", "序号", "名称", "状态　　", "配置")
	for i, ins := range instances {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t\n", i+1, *ins.DisplayName, getInstanceState(ins.LifecycleState), *ins.Shape)
	}
	w.Flush()
	fmt.Println("--------------------")
	fmt.Printf("\n\033[1;32ma: %s   b: %s   c: %s   d: %s\033[0m\n", "启动全部", "停止全部", "重启全部", "终止全部")
	var input string
	var index int
	for {
		fmt.Print("请输入序号查看实例详细信息 (输入 'q' 或直接回车返回): ")
		_, err := fmt.Scanln(&input)
		if err != nil || input == "" || strings.EqualFold(input, "q") {
			return
		}
		switch input {
		case "a":
			fmt.Printf("确定启动全部实例？(输入 y 并回车): ")
			var confirmInput string
			fmt.Scanln(&confirmInput)
			if strings.EqualFold(confirmInput, "y") {
				for _, ins := range instances {
					_, err := instanceAction(app.clients.Compute, ins.Id, core.InstanceActionActionStart)
					if err != nil {
						fmt.Printf("\033[1;31m实例 %s 启动失败.\033[0m %s\n", *ins.DisplayName, err.Error())
					} else {
						fmt.Printf("\033[1;32m实例 %s 启动成功.\033[0m\n", *ins.DisplayName)
					}
				}
			} else {
				continue
			}
			time.Sleep(1 * time.Second)
			app.listInstances()
			return
		case "b":
			fmt.Printf("确定停止全部实例？(输入 y 并回车): ")
			var confirmInput string
			fmt.Scanln(&confirmInput)
			if strings.EqualFold(confirmInput, "y") {
				for _, ins := range instances {
					_, err := instanceAction(app.clients.Compute, ins.Id, core.InstanceActionActionSoftstop)
					if err != nil {
						fmt.Printf("\033[1;31m实例 %s 停止失败.\033[0m %s\n", *ins.DisplayName, err.Error())
					} else {
						fmt.Printf("\033[1;32m实例 %s 停止成功.\033[0m\n", *ins.DisplayName)
					}
				}
			} else {
				continue
			}
			time.Sleep(1 * time.Second)
			app.listInstances()
			return
		case "c":
			fmt.Printf("确定重启全部实例？(输入 y 并回车): ")
			var confirmInput string
			fmt.Scanln(&confirmInput)
			if strings.EqualFold(confirmInput, "y") {
				for _, ins := range instances {
					_, err := instanceAction(app.clients.Compute, ins.Id, core.InstanceActionActionSoftreset)
					if err != nil {
						fmt.Printf("\033[1;31m实例 %s 重启失败.\033[0m %s\n", *ins.DisplayName, err.Error())
					} else {
						fmt.Printf("\033[1;32m实例 %s 重启成功.\033[0m\n", *ins.DisplayName)
					}
				}
			} else {
				continue
			}
			time.Sleep(1 * time.Second)
			app.listInstances()
			return
		case "d":
			fmt.Printf("确定终止全部实例？(输入 y 并回车): ")
			var confirmInput string
			fmt.Scanln(&confirmInput)
			if strings.EqualFold(confirmInput, "y") {
				for _, ins := range instances {
					err := terminateInstance(app.clients.Compute, ins.Id)
					if err != nil {
						fmt.Printf("\033[1;31m实例 %s 终止失败.\033[0m %s\n", *ins.DisplayName, err.Error())
					} else {
						fmt.Printf("\033[1;32m实例 %s 终止成功.\033[0m\n", *ins.DisplayName)
					}
				}
			} else {
				continue
			}
			time.Sleep(1 * time.Second)
			app.listInstances()
			return
		}
		index, _ = strconv.Atoi(input)
		if 0 < index && index <= len(instances) {
			break
		} else {
			input = ""
			index = 0
			fmt.Printf("\033[1;31m错误! 请输入正确的序号\033[0m\n")
		}
	}
	app.instanceDetails(instances[index-1].Id)
}
func (app *App) instanceDetails(instanceId *string) {
	for {
		fmt.Println("正在获取实例详细信息...")
		instance, err := getInstance(app.clients.Compute, instanceId)
		if err != nil {
			fmt.Printf("\033[1;31m获取实例详细信息失败, 回车返回上一级菜单.\033[0m")
			fmt.Scanln()
			app.listInstances()
			return
		}
		vnics, err := getInstanceVnics(app.clients, instanceId)
		if err != nil {
			fmt.Printf("\033[1;31m获取实例VNIC失败, 回车返回上一级菜单.\033[0m")
			fmt.Scanln()
			app.listInstances()
			return
		}
		var publicIps = make([]string, 0)
		var ipv6s = make([]string, 0)
		var subnetName string

		if len(vnics) > 0 {
			primaryVnic := vnics[0]
			for _, vnic := range vnics {
				if *vnic.IsPrimary {
					primaryVnic = vnic
				}
				if vnic.PublicIp != nil {
					publicIps = append(publicIps, *vnic.PublicIp)
				}
				// 获取 IPv6
				ipv6List, err := listIpv6s(app.clients.Network, vnic.Id)
				if err == nil {
					for _, ipv6 := range ipv6List {
						ipv6s = append(ipv6s, *ipv6.IpAddress)
					}
				}
			}
			// 获取子网信息
			subnet, err := getSubnet(app.clients.Network, primaryVnic.SubnetId)
			if err == nil {
				subnetName = *subnet.DisplayName
			}
		}
		strPublicIps := strings.Join(publicIps, ", ")
		if strPublicIps == "" {
			strPublicIps = "N/A"
		}
		strIpv6s := strings.Join(ipv6s, ", ")
		if strIpv6s == "" {
			strIpv6s = "N/A"
		}

		fmt.Printf("\n\033[1;32m实例详细信息\033[0m \n(当前账号: %s)\n\n", app.oracleSectionName)
		w := new(tabwriter.Writer)
		w.Init(os.Stdout, 0, 8, 2, '\t', 0)
		fmt.Fprintf(w, "名称:\t%s\n", *instance.DisplayName)
		fmt.Fprintf(w, "状态:\t%s\n", getInstanceState(instance.LifecycleState))
		fmt.Fprintf(w, "公共IPv4:\t%s\n", strPublicIps)
		fmt.Fprintf(w, "公共IPv6:\t%s\n", strIpv6s)
		fmt.Fprintf(w, "可用性域:\t%s\n", *instance.AvailabilityDomain)
		fmt.Fprintf(w, "子网:\t%s\n", subnetName)
		fmt.Fprintf(w, "开机时间:\t%s\n", instance.TimeCreated.Format(timeLayout))
		fmt.Fprintf(w, "配置:\t%s\n", *instance.Shape)
		fmt.Fprintf(w, "OCPU计数:\t%g\n", *instance.ShapeConfig.Ocpus)
		fmt.Fprintf(w, "网络带宽(Gbps):\t%g\n", *instance.ShapeConfig.NetworkingBandwidthInGbps)
		fmt.Fprintf(w, "内存(GB):\t%g\n\n", *instance.ShapeConfig.MemoryInGBs)
		fmt.Fprintln(w, "Oracle Cloud Agent 插件配置情况")
		fmt.Fprintf(w, "  监控插件已禁用？:\t%t\n", *instance.AgentConfig.IsMonitoringDisabled)
		fmt.Fprintf(w, "  管理插件已禁用？:\t%t\n", *instance.AgentConfig.IsManagementDisabled)
		fmt.Fprintf(w, "  所有插件均已禁用？:\t%t\n", *instance.AgentConfig.AreAllPluginsDisabled)
		for _, value := range instance.AgentConfig.PluginsConfig {
			fmt.Fprintf(w, "  %s:\t%s\n", *value.Name, value.DesiredState)
		}
		w.Flush()

		fmt.Println("--------------------")
		fmt.Printf("\n\033[1;32m1: %s   2: %s   3: %s   4: %s   5: %s\033[0m\n", "启动", "停止", "重启", "终止", "更换IPv4")
		fmt.Printf("\033[1;32m6: %s   7: %s   8: %s   9: %s   10: %s\033[0m\n", "升级/降级", "修改名称", "Agent插件配置", "查看流量", "添加IPv6")
		var input string
		var num int
		fmt.Print("\n请输入需要执行操作的序号 (输入 'q' 或直接回车返回): ")
		fmt.Scanln(&input)
		if input == "" || strings.EqualFold(input, "q") {
			app.listInstances()
			return
		}
		num, _ = strconv.Atoi(input)
		switch num {
		case 1:
			_, err := instanceAction(app.clients.Compute, instance.Id, core.InstanceActionActionStart)
			if err != nil {
				fmt.Printf("\033[1;31m启动实例失败.\033[0m %s\n", err.Error())
			} else {
				fmt.Printf("\033[1;32m正在启动实例, 请稍后查看实例状态\033[0m\n")
			}
			time.Sleep(1 * time.Second)

		case 2:
			_, err := instanceAction(app.clients.Compute, instance.Id, core.InstanceActionActionSoftstop)
			if err != nil {
				fmt.Printf("\033[1;31m停止实例失败.\033[0m %s\n", err.Error())
			} else {
				fmt.Printf("\033[1;32m正在停止实例, 请稍后查看实例状态\033[0m\n")
			}
			time.Sleep(1 * time.Second)

		case 3:
			_, err := instanceAction(app.clients.Compute, instance.Id, core.InstanceActionActionSoftreset)
			if err != nil {
				fmt.Printf("\033[1;31m重启实例失败.\033[0m %s\n", err.Error())
			} else {
				fmt.Printf("\033[1;32m正在重启实例, 请稍后查看实例状态\033[0m\n")
			}
			time.Sleep(1 * time.Second)

		case 4:
			fmt.Printf("确定终止实例？(输入 y 并回车): ")
			var confirmInput string
			fmt.Scanln(&confirmInput)
			if strings.EqualFold(confirmInput, "y") {
				err := terminateInstance(app.clients.Compute, instance.Id)
				if err != nil {
					fmt.Printf("\033[1;31m终止实例失败.\033[0m %s\n", err.Error())
				} else {
					fmt.Printf("\033[1;32m正在终止实例, 请稍后查看实例状态\033[0m\n")
				}
				time.Sleep(1 * time.Second)
			}

		case 5:
			if len(vnics) == 0 {
				fmt.Printf("\033[1;31m实例已终止或获取实例VNIC失败，请稍后重试.\033[0m\n")
				break
			}
			fmt.Printf("将删除当前公共IP并创建一个新的公共IP。确定更换实例公共IP？(输入 y 并回车): ")
			var confirmInput string
			fmt.Scanln(&confirmInput)
			if strings.EqualFold(confirmInput, "y") {
				publicIp, err := changePublicIp(app.clients, vnics)
				if err != nil {
					fmt.Printf("\033[1;31m更换实例公共IP失败.\033[0m %s\n", err.Error())
				} else {
					fmt.Printf("\033[1;32m更换实例公共IP成功, 实例公共IP: \033[0m%s\n", *publicIp.IpAddress)
				}
				time.Sleep(1 * time.Second)
			}

		case 6:
			fmt.Printf("升级/降级实例, 请输入CPU个数: ")
			var ocpusInput string
			var ocpus float32
			var memoryInGBs float32
			fmt.Scanln(&ocpusInput)
			value, _ := strconv.ParseFloat(ocpusInput, 32)
			ocpus = float32(value)
			memoryInput := ""
			fmt.Printf("升级/降级实例, 请输入内存大小: ")
			fmt.Scanln(&memoryInput)
			value, _ = strconv.ParseFloat(memoryInput, 32)
			memoryInGBs = float32(value)
			fmt.Println("正在升级/降级实例...")
			_, err := updateInstance(app.clients.Compute, instance.Id, nil, &ocpus, &memoryInGBs, nil, nil)
			if err != nil {
				fmt.Printf("\033[1;31m升级/降级实例失败.\033[0m %s\n", err.Error())
			} else {
				fmt.Printf("\033[1;32m升级/降级实例成功.\033[0m\n")
			}
			time.Sleep(1 * time.Second)

		case 7:
			fmt.Printf("请为实例输入一个新的名称: ")
			var newName string
			fmt.Scanln(&newName)
			fmt.Println("正在修改实例名称...")
			_, err := updateInstance(app.clients.Compute, instance.Id, &newName, nil, nil, nil, nil)
			if err != nil {
				fmt.Printf("\033[1;31m修改实例名称失败.\033[0m %s\n", err.Error())
			} else {
				fmt.Printf("\033[1;32m修改实例名称成功.\033[0m\n")
			}
			time.Sleep(1 * time.Second)

		case 8:
			fmt.Printf("Oracle Cloud Agent 插件配置, 请输入 (1: 启用管理和监控插件; 2: 禁用管理和监控插件): ")
			var agentInput string
			fmt.Scanln(&agentInput)
			if agentInput == "1" {
				disable := false
				_, err := updateInstance(app.clients.Compute, instance.Id, nil, nil, nil, instance.AgentConfig.PluginsConfig, &disable)
				if err != nil {
					fmt.Printf("\033[1;31m启用管理和监控插件失败.\033[0m %s\n", err.Error())
				} else {
					fmt.Printf("\033[1;32m启用管理和监控插件成功.\033[0m\n")
				}
			} else if agentInput == "2" {
				disable := true
				_, err := updateInstance(app.clients.Compute, instance.Id, nil, nil, nil, instance.AgentConfig.PluginsConfig, &disable)
				if err != nil {
					fmt.Printf("\033[1;31m禁用管理和监控插件失败.\033[0m %s\n", err.Error())
				} else {
					fmt.Printf("\033[1;32m禁用管理和监控插件成功.\033[0m\n")
				}
			} else {
				fmt.Printf("\033[1;31m输入错误.\033[0m\n")
			}
			time.Sleep(1 * time.Second)

		case 9:
			app.viewInstanceTraffic(instance.Id)
		case 10:
			app.addIpv6ToInstance(vnics)

		default:
			app.listInstances()
			return
		}
	}
}
func (app *App) listBootVolumes() {
	var bootVolumes []core.BootVolume
	var wg sync.WaitGroup
	for _, ad := range app.availabilityDomains {
		wg.Add(1)
		go func(adName *string) {
			defer wg.Done()
			volumes, err := getBootVolumes(app.clients.Storage, adName, &app.oracleConfig.Tenancy)
			if err != nil {
				printlnErr("获取引导卷失败", err.Error())
			} else {
				bootVolumes = append(bootVolumes, volumes...)
			}
		}(ad.Name)
	}
	wg.Wait()

	fmt.Printf("\n\033[1;32m引导卷\033[0m \n(当前账号: %s)\n\n", app.oracleSectionName)
	w := new(tabwriter.Writer)
	w.Init(os.Stdout, 4, 8, 1, '\t', 0)
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t\n", "序号", "名称", "状态　　", "大小(GB)")
	for i, volume := range bootVolumes {
		fmt.Fprintf(w, "%d\t%s\t%s\t%d\t\n", i+1, *volume.DisplayName, getBootVolumeState(volume.LifecycleState), *volume.SizeInGBs)
	}
	w.Flush()
	fmt.Printf("\n")
	var input string
	var index int
	for {
		fmt.Print("请输入序号查看引导卷详细信息 (输入 'q' 或直接回车返回): ")
		_, err := fmt.Scanln(&input)
		if err != nil || input == "" || strings.EqualFold(input, "q") {
			return
		}
		index, _ = strconv.Atoi(input)
		if 0 < index && index <= len(bootVolumes) {
			break
		} else {
			input = ""
			index = 0
			fmt.Printf("\033[1;31m错误! 请输入正确的序号\033[0m\n")
		}
	}
	app.bootvolumeDetails(bootVolumes[index-1].Id)
}
func (app *App) bootvolumeDetails(bootVolumeId *string) {
	for {
		fmt.Println("正在获取引导卷详细信息...")
		bootVolume, err := getBootVolume(app.clients.Storage, bootVolumeId)
		if err != nil {
			fmt.Printf("\033[1;31m获取引导卷详细信息失败, 回车返回上一级菜单.\033[0m")
			fmt.Scanln()
			app.listBootVolumes()
			return
		}

		attachments, err := listBootVolumeAttachments(app.clients.Compute, bootVolume.AvailabilityDomain, bootVolume.CompartmentId, bootVolume.Id)
		attachIns := make([]string, 0)
		if err != nil {
			attachIns = append(attachIns, err.Error())
		} else {
			for _, attachment := range attachments {
				ins, err := getInstance(app.clients.Compute, attachment.InstanceId)
				if err != nil {
					attachIns = append(attachIns, err.Error())
				} else {
					attachIns = append(attachIns, *ins.DisplayName)
				}
			}
		}

		var performance string
		if bootVolume.VpusPerGB != nil {
			switch *bootVolume.VpusPerGB {
			case 10:
				performance = fmt.Sprintf("均衡 (VPU:%d)", *bootVolume.VpusPerGB)
			case 20:
				performance = fmt.Sprintf("性能较高 (VPU:%d)", *bootVolume.VpusPerGB)
			default:
				performance = fmt.Sprintf("UHP (VPU:%d)", *bootVolume.VpusPerGB)
			}
		} else {
			performance = "N/A"
		}

		fmt.Printf("\n\033[1;32m引导卷详细信息\033[0m \n(当前账号: %s)\n\n", app.oracleSectionName)
		w := new(tabwriter.Writer)
		w.Init(os.Stdout, 0, 8, 2, '\t', 0)
		fmt.Fprintf(w, "名称:\t%s\n", *bootVolume.DisplayName)
		fmt.Fprintf(w, "状态:\t%s\n", getBootVolumeState(bootVolume.LifecycleState))
		fmt.Fprintf(w, "可用性域:\t%s\n", *bootVolume.AvailabilityDomain)
		fmt.Fprintf(w, "大小(GB):\t%d\n", *bootVolume.SizeInGBs)
		fmt.Fprintf(w, "性能:\t%s\n", performance)
		fmt.Fprintf(w, "附加的实例:\t%s\n", strings.Join(attachIns, ","))
		w.Flush()
		fmt.Println("--------------------")
		fmt.Printf("\n\033[1;32m1: %s   2: %s   3: %s   4: %s\033[0m\n", "修改性能", "修改大小", "分离引导卷", "终止引导卷")
		var input string
		var num int
		fmt.Print("\n请输入需要执行操作的序号 (输入 'q' 或直接回车返回): ")
		fmt.Scanln(&input)
		if input == "" || strings.EqualFold(input, "q") {
			app.listBootVolumes()
			return
		}
		num, _ = strconv.Atoi(input)
		switch num {
		case 1:
			fmt.Printf("修改引导卷性能, 请输入 (1: 均衡; 2: 性能较高): ")
			var perfInput string
			fmt.Scanln(&perfInput)
			if perfInput == "1" {
				_, err := updateBootVolume(app.clients.Storage, bootVolume.Id, nil, common.Int64(10))
				if err != nil {
					fmt.Printf("\033[1;31m修改引导卷性能失败.\033[0m %s\n", err.Error())
				} else {
					fmt.Printf("\033[1;32m修改引导卷性能成功, 请稍后查看引导卷状态\033[0m\n")
				}
			} else if perfInput == "2" {
				_, err := updateBootVolume(app.clients.Storage, bootVolume.Id, nil, common.Int64(20))
				if err != nil {
					fmt.Printf("\033[1;31m修改引导卷性能失败.\033[0m %s\n", err.Error())
				} else {
					fmt.Printf("\033[1;32m修改引导卷性能成功, 请稍后查看引导卷信息\033[0m\n")
				}
			} else {
				fmt.Printf("\033[1;31m输入错误.\033[0m\n")
			}
			time.Sleep(1 * time.Second)

		case 2:
			fmt.Printf("修改引导卷大小, 请输入 (例如修改为50GB, 输入50): ")
			var sizeInput string
			var sizeInGBs int64
			fmt.Scanln(&sizeInput)
			sizeInGBs, _ = strconv.ParseInt(sizeInput, 10, 64)
			if sizeInGBs > 0 {
				_, err := updateBootVolume(app.clients.Storage, bootVolume.Id, &sizeInGBs, nil)
				if err != nil {
					fmt.Printf("\033[1;31m修改引导卷大小失败.\033[0m %s\n", err.Error())
				} else {
					fmt.Printf("\033[1;32m修改引导卷大小成功, 请稍后查看引导卷信息\033[0m\n")
				}
			} else {
				fmt.Printf("\033[1;31m输入错误.\033[0m\n")
			}
			time.Sleep(1 * time.Second)

		case 3:
			fmt.Printf("确定分离引导卷？(输入 y 并回车): ")
			var confirmInput string
			fmt.Scanln(&confirmInput)
			if strings.EqualFold(confirmInput, "y") {
				for _, attachment := range attachments {
					_, err := detachBootVolume(app.clients.Compute, attachment.Id)
					if err != nil {
						fmt.Printf("\033[1;31m分离引导卷失败.\033[0m %s\n", err.Error())
					} else {
						fmt.Printf("\033[1;32m分离引导卷成功, 请稍后查看引导卷信息\033[0m\n")
					}
				}
			}
			time.Sleep(1 * time.Second)

		case 4:
			fmt.Printf("确定终止引导卷？(输入 y 并回车): ")
			var confirmInput string
			fmt.Scanln(&confirmInput)
			if strings.EqualFold(confirmInput, "y") {
				_, err := deleteBootVolume(app.clients.Storage, bootVolume.Id)
				if err != nil {
					fmt.Printf("\033[1;31m终止引导卷失败.\033[0m %s\n", err.Error())
				} else {
					fmt.Printf("\033[1;32m终止引导卷成功, 请稍后查看引导卷信息\033[0m\n")
				}

			}
			time.Sleep(1 * time.Second)

		default:
			app.listBootVolumes()
			return
		}
	}
}
func (app *App) listLaunchInstanceTemplates() {
	var instanceSections []*ini.Section
	instanceSections = append(instanceSections, app.instanceBaseSection.ChildSections()...)
	instanceSections = append(instanceSections, app.oracleSection.ChildSections()...)
	if len(instanceSections) == 0 {
		fmt.Printf("\033[1;31m未找到实例模版, 回车返回上一级菜单.\033[0m")
		fmt.Scanln()
		return
	}

	for {
		fmt.Printf("\n\033[1;32m选择对应的实例模版开始创建实例\033[0m \n(当前账号: %s)\n\n", app.oracleSectionName)
		w := new(tabwriter.Writer)
		w.Init(os.Stdout, 4, 8, 1, '\t', 0)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t\n", "序号", "配置", "CPU个数", "内存(GB)")
		for i, instanceSec := range instanceSections {
			cpu := instanceSec.Key("cpus").Value()
			if cpu == "" {
				cpu = "-"
			}
			memory := instanceSec.Key("memoryInGBs").Value()
			if memory == "" {
				memory = "-"
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t\n", i+1, instanceSec.Key("shape").Value(), cpu, memory)
		}
		w.Flush()
		fmt.Printf("\n")
		var input string
		var index int
		for {
			fmt.Print("请输入需要创建的实例的序号 (输入 'q' 或直接回车返回): ")
			_, err := fmt.Scanln(&input)
			if err != nil || input == "" || strings.EqualFold(input, "q") {
				return
			}
			index, _ = strconv.Atoi(input)
			if 0 < index && index <= len(instanceSections) {
				break
			} else {
				input = ""
				index = 0
				fmt.Printf("\033[1;31m错误! 请输入正确的序号\033[0m\n")
			}
		}

		instanceSection := instanceSections[index-1]
		instance := Instance{}
		err := instanceSection.MapTo(&instance)
		if err != nil {
			printlnErr("解析实例模版参数失败", err.Error())
			continue
		}

		// 此调用需要适应CLI模式（如果使用任务）
		// 目前，在CLI模式下它不会被取消。
		taskCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		task := &CreationTask{ID: "cli_task"} // CLI的虚拟任务
		app.LaunchInstances(taskCtx, task, app.availabilityDomains, instance)
	}

}
func (app *App) multiBatchLaunchInstances() {
	IPsFilePath := IPsFilePrefix + "-" + time.Now().Format("2006-01-02-150405.txt")
	for _, sec := range app.oracleSections {
		var err error
		err = app.initializeClients(sec)
		if err != nil {
			continue
		}

		app.batchLaunchInstances(sec)
		app.batchListInstancesIp(IPsFilePath, sec)
		command(appConfig.cmd)
		sleepRandomSecond(5, 5)
	}
}
func (app *App) batchLaunchInstances(oracleSec *ini.Section) {
	var instanceSections []*ini.Section
	instanceSections = append(instanceSections, app.instanceBaseSection.ChildSections()...)
	instanceSections = append(instanceSections, oracleSec.ChildSections()...)
	if len(instanceSections) == 0 {
		return
	}

	printf("\033[1;36m[%s] 开始创建\033[0m\n", app.oracleSectionName)
	sendMessage(appConfig.chat_id, fmt.Sprintf("[%s]", app.oracleSectionName), "开始创建", nil)

	var SUM, NUM int32 = 0, 0
	for _, instanceSec := range instanceSections {
		instance := Instance{}
		err := instanceSec.MapTo(&instance)
		if err != nil {
			printlnErr("解析实例模版参数失败", err.Error())
			continue
		}
		taskCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		task := &CreationTask{ID: "cli_batch_task"} // 虚拟任务
		app.LaunchInstances(taskCtx, task, app.availabilityDomains, instance)
		SUM += task.TotalCount
		NUM += task.SuccessCount
	}
	printf("\033[1;36m[%s] 结束创建。创建实例总数: %d, 成功 %d , 失败 %d\033[0m\n", app.oracleSectionName, SUM, NUM, SUM-NUM)
	text := fmt.Sprintf("结束创建。创建实例总数: %d, 成功 %d , 失败 %d", SUM, NUM, SUM-NUM)
	sendMessage(appConfig.chat_id, fmt.Sprintf("[%s]", app.oracleSectionName), text, nil)
}
func (app *App) multiBatchListInstancesIp() {
	IPsFilePath := IPsFilePrefix + "-" + time.Now().Format("2006-01-02-150405.txt")
	_, err := os.Stat(IPsFilePath)
	if err != nil && os.IsNotExist(err) {
		os.Create(IPsFilePath)
	}

	fmt.Printf("正在导出实例公共IP地址...\n")
	for _, sec := range app.oracleSections {
		err := app.initializeClients(sec)
		if err != nil {
			continue
		}
		app.ListInstancesIPs(IPsFilePath, sec.Name())
	}
	fmt.Printf("导出实例公共IP地址完成，请查看文件 %s\n", IPsFilePath)
}
func (app *App) batchListInstancesIp(filePath string, sec *ini.Section) {
	_, err := os.Stat(filePath)
	if err != nil && os.IsNotExist(err) {
		os.Create(filePath)
	}
	fmt.Printf("正在导出实例公共IP地址...\n")
	app.ListInstancesIPs(filePath, sec.Name())
	fmt.Printf("导出实例IP地址完成，请查看文件 %s\n", filePath)
}
func (app *App) ListInstancesIPs(filePath string, sectionName string) {
	var vnicAttachments []core.VnicAttachment
	var vas []core.VnicAttachment
	var nextPage *string
	var err error
	for {
		vas, nextPage, err = ListVnicAttachments(ctx, app.clients.Compute, &app.oracleConfig.Tenancy, nil, nextPage)
		if err == nil {
			vnicAttachments = append(vnicAttachments, vas...)
		}
		if nextPage == nil || len(vas) == 0 {
			break
		}
	}

	if err != nil {
		fmt.Printf("ListVnicAttachments 错误: %s\n", err.Error())
		return
	}
	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_WRONLY, os.ModeAppend)
	if err != nil {
		fmt.Printf("打开文件失败, 错误: %s\n", err.Error())
		return
	}
	_, err = io.WriteString(file, "["+sectionName+"]\n")
	if err != nil {
		fmt.Printf("%s\n", err.Error())
	}
	for _, vnicAttachment := range vnicAttachments {
		vnic, err := GetVnic(app.clients.Network, vnicAttachment.VnicId)
		if err != nil {
			fmt.Printf("IP地址获取失败, %s\n", err.Error())
			continue
		}
		if vnic.PublicIp != nil && *vnic.PublicIp != "" {
			fmt.Printf("[%s] 实例: %s, IP: %s\n", sectionName, *vnic.DisplayName, *vnic.PublicIp)
			_, err = io.WriteString(file, "实例: "+*vnic.DisplayName+", IP: "+*vnic.PublicIp+"\n")
			if err != nil {
				fmt.Printf("写入文件失败, 错误: %s\n", err.Error())
			}
		}
	}
	_, err = io.WriteString(file, "\n")
	if err != nil {
		fmt.Printf("%s\n", err.Error())
	}
}
func sleepRandomSecond(min, max int32) int32 {
	if min <= 0 || max <= 0 {
		return 1
	}
	if min >= max {
		return max
	}
	second := rand.Int31n(max-min) + min
	printf("休眠 %d 秒...\n", second)
	return second
}
func getProvider(oracle Oracle) (common.ConfigurationProvider, error) {
	content, err := ioutil.ReadFile(oracle.Key_file)
	if err != nil {
		return nil, err
	}
	privateKey := string(content)
	privateKeyPassphrase := common.String(oracle.Key_password)
	return common.NewRawConfigurationProvider(oracle.Tenancy, oracle.User, oracle.Region, oracle.Fingerprint, privateKey, privateKeyPassphrase), nil
}
func CreateOrGetNetworkInfrastructure(ctx context.Context, c core.VirtualNetworkClient, tenancyId *string, instance Instance) (subnet core.Subnet, err error) {
	var vcn core.Vcn
	vcn, err = createOrGetVcn(ctx, c, tenancyId, instance)
	if err != nil {
		return
	}
	var gateway core.InternetGateway
	gateway, err = createOrGetInternetGateway(c, vcn.Id, tenancyId)
	if err != nil {
		return
	}
	_, err = createOrGetRouteTable(c, gateway.Id, vcn.Id, tenancyId)
	if err != nil {
		return
	}
	subnet, err = createOrGetSubnetWithDetails(
		ctx, c, vcn.Id,
		common.String(instance.SubnetDisplayName),
		common.String("10.0.0.0/20"),
		common.String("subnetdns"),
		common.String(instance.AvailabilityDomain),
		tenancyId)
	return
}
func createOrGetSubnetWithDetails(ctx context.Context, c core.VirtualNetworkClient, vcnID *string,
	displayName *string, cidrBlock *string, dnsLabel *string, availableDomain *string, tenancyId *string) (subnet core.Subnet, err error) {

	// 1. 列出VCN中已有的子网
	var subnets []core.Subnet
	subnets, err = listSubnets(ctx, c, vcnID, tenancyId)
	if err != nil {
		return subnet, fmt.Errorf("列出子网失败: %w", err)
	}

	// 2. 如果在配置中提供了特定的显示名称，则尝试查找它。
	if displayName != nil && *displayName != "" {
		for _, s := range subnets {
			if s.DisplayName != nil && *s.DisplayName == *displayName {
				fmt.Printf("找到并复用已存在的子网: %s\n", *s.DisplayName)
				return s, nil
			}
		}
		// 如果按名称找不到，将继续执行创建逻辑。
	} else {
		// 3. 如果未提供名称，并且存在子网，则复用找到的第一个子网。
		if len(subnets) > 0 {
			fmt.Printf("未指定子网名称，找到并复用第一个可用的子网: %s\n", *subnets[0].DisplayName)
			return subnets[0], nil
		}
	}

	// 4. 如果找不到合适的子网，则继续创建一个新的。
	fmt.Printf("开始创建Subnet（没有可用的Subnet，或指定的Subnet不存在）\n")

	// 如果未提供名称，则为新子网生成一个名称。
	var creationDisplayName *string
	if displayName != nil && *displayName != "" {
		creationDisplayName = displayName
	} else {
		creationDisplayName = common.String("subnet-" + time.Now().Format("20060102-1504"))
	}

	// 5. 尝试创建子网，通过尝试后续的块来处理CIDR冲突。
	baseCidr := "10.0.0.0/20"
	if cidrBlock != nil && *cidrBlock != "" {
		baseCidr = *cidrBlock
	}

	for i := 0; i < 16; i++ { // 对于 /16 VCN范围，最多尝试16次
		request := core.CreateSubnetRequest{
			CreateSubnetDetails: core.CreateSubnetDetails{
				CompartmentId: tenancyId,
				CidrBlock:     &baseCidr,
				DisplayName:   creationDisplayName,
				DnsLabel:      dnsLabel,
				VcnId:         vcnID,
			},
			RequestMetadata: getCustomRequestMetadataWithRetryPolicy(),
		}

		var r core.CreateSubnetResponse
		r, err = c.CreateSubnet(ctx, request)

		if err == nil {
			// 成功，现在轮询直到子网状态变为'Available'
			pollUntilAvailable := func(r common.OCIOperationResponse) bool {
				if converted, ok := r.Response.(core.GetSubnetResponse); ok {
					return converted.LifecycleState != core.SubnetLifecycleStateAvailable
				}
				return true
			}

			pollGetRequest := core.GetSubnetRequest{
				SubnetId:        r.Id,
				RequestMetadata: helpers.GetRequestMetadataWithCustomizedRetryPolicy(pollUntilAvailable),
			}

			_, err = c.GetSubnet(ctx, pollGetRequest)
			if err != nil {
				return subnet, err // 返回轮询错误
			}

			// 更新安全列表以允许所有入站流量
			getReq := core.GetSecurityListRequest{
				SecurityListId:  common.String(r.SecurityListIds[0]),
				RequestMetadata: getCustomRequestMetadataWithRetryPolicy(),
			}

			var getResp core.GetSecurityListResponse
			getResp, err = c.GetSecurityList(ctx, getReq)
			if err != nil {
				return subnet, err // 返回获取安全列表的错误
			}

			newRules := append(getResp.IngressSecurityRules, core.IngressSecurityRule{
				Protocol: common.String("all"), // 允许所有协议
				Source:   common.String("0.0.0.0/0"),
			})

			updateReq := core.UpdateSecurityListRequest{
				SecurityListId: common.String(r.SecurityListIds[0]),
				UpdateSecurityListDetails: core.UpdateSecurityListDetails{
					IngressSecurityRules: newRules,
				},
				RequestMetadata: getCustomRequestMetadataWithRetryPolicy(),
			}

			_, err = c.UpdateSecurityList(ctx, updateReq)
			if err != nil {
				return subnet, err // 返回更新安全列表的错误
			}
			fmt.Printf("Subnet创建成功: %s\n", *r.Subnet.DisplayName)
			subnet = r.Subnet
			return subnet, nil // 返回成功创建的子网
		}

		// 检查错误是否为CIDR重叠冲突
		if serviceError, ok := common.IsServiceError(err); ok {
			if serviceError.GetHTTPStatusCode() == 400 && strings.Contains(serviceError.GetMessage(), "overlaps with this CIDR") {
				printf("CIDR %s 重叠。正在尝试下一个 CIDR...\n", baseCidr)

				// 计算下一个CIDR块。对于 /20，第三个八位字节增加16。
				var ip net.IP
				var ipnet *net.IPNet
				ip, ipnet, err = net.ParseCIDR(baseCidr)
				if err != nil {
					return subnet, fmt.Errorf("为重试逻辑解析CIDR失败: %w", err)
				}
				ip = ip.To4()
				if ip == nil {
					return subnet, errors.New("为重试逻辑解析CIDR到IPv4失败")
				}

				// 递增第三个八位字节。
				ip[2] += 16

				// 检查第三个八位字节是否溢出 (例如, 10.0.240.0 -> 10.1.0.0)
				if ip[2] < 16 { // 它环绕了
					return subnet, errors.New("在 10.0.0.0/16 范围内已无可用CIDR块")
				}

				// 使用相同的掩码大小重构CIDR字符串
				_, mask := ipnet.Mask.Size()
				baseCidr = fmt.Sprintf("%s/%d", ip.String(), mask)
				continue // 使用新的CIDR重试
			}
		}

		// 如果是其他不可恢复的错误，则立即失败
		return subnet, err
	}

	// 如果循环完成而没有成功创建子网
	return subnet, fmt.Errorf("多次尝试后创建子网失败: %w", err)
}
func listSubnets(ctx context.Context, c core.VirtualNetworkClient, vcnID, tenancyId *string) (subnets []core.Subnet, err error) {
	request := core.ListSubnetsRequest{
		CompartmentId:   tenancyId,
		VcnId:           vcnID,
		RequestMetadata: getCustomRequestMetadataWithRetryPolicy(),
	}
	var r core.ListSubnetsResponse
	r, err = c.ListSubnets(ctx, request)
	if err != nil {
		return
	}
	subnets = r.Items
	return
}
func createOrGetVcn(ctx context.Context, c core.VirtualNetworkClient, tenancyId *string, instance Instance) (core.Vcn, error) {
	var vcn core.Vcn
	vcnItems, err := listVcns(ctx, c, tenancyId)
	if err != nil {
		return vcn, err
	}
	displayName := common.String(instance.VcnDisplayName)
	if len(vcnItems) > 0 && *displayName == "" {
		vcn = vcnItems[0]
		return vcn, err
	}
	for _, element := range vcnItems {
		if *element.DisplayName == instance.VcnDisplayName {
			// VCN已存在，返回它
			vcn = element
			return vcn, err
		}
	}
	// 创建一个新的VCN
	fmt.Println("开始创建VCN（没有可用的VCN，或指定的VCN不存在）")
	if *displayName == "" {
		displayName = common.String(time.Now().Format("vcn-20060102-1504"))
	}
	request := core.CreateVcnRequest{}
	request.RequestMetadata = getCustomRequestMetadataWithRetryPolicy()
	request.CidrBlock = common.String("10.0.0.0/16")
	request.CompartmentId = tenancyId
	request.DisplayName = displayName
	request.DnsLabel = common.String("vcndns")
	r, err := c.CreateVcn(ctx, request)
	if err != nil {
		return vcn, err
	}
	fmt.Printf("VCN创建成功: %s\n", *r.Vcn.DisplayName)
	vcn = r.Vcn
	return vcn, err
}
func listVcns(ctx context.Context, c core.VirtualNetworkClient, tenancyId *string) ([]core.Vcn, error) {
	request := core.ListVcnsRequest{
		CompartmentId:   tenancyId,
		RequestMetadata: getCustomRequestMetadataWithRetryPolicy(),
	}
	r, err := c.ListVcns(ctx, request)
	if err != nil {
		return nil, err
	}
	return r.Items, err
}
func createOrGetInternetGateway(c core.VirtualNetworkClient, vcnID, tenancyId *string) (core.InternetGateway, error) {
	//列出网关
	var gateway core.InternetGateway
	listGWRequest := core.ListInternetGatewaysRequest{
		CompartmentId:   tenancyId,
		VcnId:           vcnID,
		RequestMetadata: getCustomRequestMetadataWithRetryPolicy(),
	}

	listGWRespone, err := c.ListInternetGateways(ctx, listGWRequest)
	if err != nil {
		fmt.Printf("Internet网关列表错误: %s\n", err.Error())
		return gateway, err
	}

	if len(listGWRespone.Items) >= 1 {
		// 网关已存在
		gateway = listGWRespone.Items[0]
	} else {
		// 创建新网关
		fmt.Printf("开始创建Internet网关\n")
		enabled := true
		createGWDetails := core.CreateInternetGatewayDetails{
			CompartmentId: tenancyId,
			IsEnabled:     &enabled,
			VcnId:         vcnID,
		}

		createGWRequest := core.CreateInternetGatewayRequest{
			CreateInternetGatewayDetails: createGWDetails,
			RequestMetadata:              getCustomRequestMetadataWithRetryPolicy()}

		createGWResponse, err := c.CreateInternetGateway(ctx, createGWRequest)

		if err != nil {
			fmt.Printf("Internet网关创建错误: %s\n", err.Error())
			return gateway, err
		}
		gateway = createGWResponse.InternetGateway
		fmt.Printf("Internet网关创建成功: %s\n", *gateway.DisplayName)
	}
	return gateway, err
}
func createOrGetRouteTable(c core.VirtualNetworkClient, gatewayID, VcnID, tenancyId *string) (routeTable core.RouteTable, err error) {
	//列出路由表
	listRTRequest := core.ListRouteTablesRequest{
		CompartmentId:   tenancyId,
		VcnId:           VcnID,
		RequestMetadata: getCustomRequestMetadataWithRetryPolicy(),
	}
	var listRTResponse core.ListRouteTablesResponse
	listRTResponse, err = c.ListRouteTables(ctx, listRTRequest)
	if err != nil {
		fmt.Printf("路由表列表错误: %s\n", err.Error())
		return
	}

	cidrRange := "0.0.0.0/0"
	rr := core.RouteRule{
		NetworkEntityId: gatewayID,
		Destination:     &cidrRange,
		DestinationType: core.RouteRuleDestinationTypeCidrBlock,
	}

	if len(listRTResponse.Items) >= 1 {
		// 找到默认路由表并且至少有1条路由规则
		if len(listRTResponse.Items[0].RouteRules) >= 1 {
			routeTable = listRTResponse.Items[0]
			// 默认路由表需要添加路由规则
		} else {
			fmt.Printf("路由表未添加规则，开始添加Internet路由规则\n")
			updateRTDetails := core.UpdateRouteTableDetails{
				RouteRules: []core.RouteRule{rr},
			}

			updateRTRequest := core.UpdateRouteTableRequest{
				RtId:                    listRTResponse.Items[0].Id,
				UpdateRouteTableDetails: updateRTDetails,
				RequestMetadata:         getCustomRequestMetadataWithRetryPolicy(),
			}
			var updateRTResponse core.UpdateRouteTableResponse
			updateRTResponse, err = c.UpdateRouteTable(ctx, updateRTRequest)
			if err != nil {
				fmt.Printf("更新路由表时出错: %s\n", err)
				return
			}
			fmt.Printf("Internet路由规则添加成功\n")
			routeTable = updateRTResponse.RouteTable
		}

	} else {
		// 未找到默认路由表
		fmt.Printf("错误，找不到VCN默认路由表, VCN OCID: %s\n", *VcnID)
	}
	return
}
func GetImage(ctx context.Context, c core.ComputeClient, tenancyId *string, os, osVersion, shape string) (image core.Image, err error) {
	var images []core.Image
	images, err = listImages(ctx, c, tenancyId, os, osVersion, shape)
	if err != nil {
		return
	}
	if len(images) > 0 {
		image = images[0]
	} else {
		err = fmt.Errorf("未找到[%s %s]的镜像, 或该镜像不支持[%s]", os, osVersion, shape)
	}
	return
}
func listImages(ctx context.Context, c core.ComputeClient, tenancyId *string, os, osVersion, shape string) ([]core.Image, error) {
	if os == "" || osVersion == "" {
		return nil, errors.New("操作系统类型和版本不能为空, 请检查配置文件")
	}
	request := core.ListImagesRequest{
		CompartmentId:          tenancyId,
		OperatingSystem:        common.String(os),
		OperatingSystemVersion: common.String(osVersion),
		Shape:                  common.String(shape),
		RequestMetadata:        getCustomRequestMetadataWithRetryPolicy(),
	}
	r, err := c.ListImages(ctx, request)
	return r.Items, err
}
func getShape(c core.ComputeClient, imageId *string, shapeName string, tenancyId *string) (core.Shape, error) {
	var shape core.Shape
	shapes, err := listShapes(ctx, c, imageId, tenancyId)
	if err != nil {
		return shape, err
	}
	for _, s := range shapes {
		if strings.EqualFold(*s.Shape, shapeName) {
			shape = s
			return shape, nil
		}
	}
	err = errors.New("没有符合条件的Shape")
	return shape, err
}
func listShapes(ctx context.Context, c core.ComputeClient, imageID, tenancyId *string) ([]core.Shape, error) {
	request := core.ListShapesRequest{
		CompartmentId:   tenancyId,
		ImageId:         imageID,
		RequestMetadata: getCustomRequestMetadataWithRetryPolicy(),
	}
	r, err := c.ListShapes(ctx, request)
	if err == nil && (r.Items == nil || len(r.Items) == 0) {
		err = errors.New("没有符合条件的Shape")
	}
	return r.Items, err
}
func ListAvailabilityDomains(clients *OciClients) ([]identity.AvailabilityDomain, error) {
	tenancyId, err := clients.Provider.TenancyOCID()
	if err != nil {
		return nil, err
	}
	req := identity.ListAvailabilityDomainsRequest{
		CompartmentId:   &tenancyId,
		RequestMetadata: getCustomRequestMetadataWithRetryPolicy(),
	}
	resp, err := clients.Identity.ListAvailabilityDomains(ctx, req)
	return resp.Items, err
}
func ListInstances(ctx context.Context, c core.ComputeClient, tenancyId *string, page *string) ([]core.Instance, *string, error) {
	req := core.ListInstancesRequest{
		CompartmentId:   tenancyId,
		RequestMetadata: getCustomRequestMetadataWithRetryPolicy(),
		Limit:           common.Int(100),
		Page:            page,
	}
	resp, err := c.ListInstances(ctx, req)
	return resp.Items, resp.OpcNextPage, err
}
func ListVnicAttachments(ctx context.Context, c core.ComputeClient, tenancyId *string, instanceId *string, page *string) ([]core.VnicAttachment, *string, error) {
	req := core.ListVnicAttachmentsRequest{
		CompartmentId:   tenancyId,
		RequestMetadata: getCustomRequestMetadataWithRetryPolicy(),
		Limit:           common.Int(100),
		Page:            page,
	}
	if instanceId != nil && *instanceId != "" {
		req.InstanceId = instanceId
	}
	resp, err := c.ListVnicAttachments(ctx, req)
	return resp.Items, resp.OpcNextPage, err
}
func GetVnic(c core.VirtualNetworkClient, vnicID *string) (core.Vnic, error) {
	req := core.GetVnicRequest{
		VnicId:          vnicID,
		RequestMetadata: getCustomRequestMetadataWithRetryPolicy(),
	}
	resp, err := c.GetVnic(ctx, req)
	if err != nil && resp.RawResponse != nil {
		err = errors.New(resp.RawResponse.Status)
	}
	return resp.Vnic, err
}
func terminateInstance(c core.ComputeClient, id *string) error {
	request := core.TerminateInstanceRequest{
		InstanceId:         id,
		PreserveBootVolume: common.Bool(false),
		RequestMetadata:    getCustomRequestMetadataWithRetryPolicy(),
	}
	_, err := c.TerminateInstance(ctx, request)
	return err
}
func getInstance(c core.ComputeClient, instanceId *string) (core.Instance, error) {
	req := core.GetInstanceRequest{
		InstanceId:      instanceId,
		RequestMetadata: getCustomRequestMetadataWithRetryPolicy(),
	}
	resp, err := c.GetInstance(ctx, req)
	return resp.Instance, err
}
func updateInstance(c core.ComputeClient, instanceId *string, displayName *string, ocpus, memoryInGBs *float32,
	details []core.InstanceAgentPluginConfigDetails, disable *bool) (core.UpdateInstanceResponse, error) {
	updateInstanceDetails := core.UpdateInstanceDetails{}
	if displayName != nil && *displayName != "" {
		updateInstanceDetails.DisplayName = displayName
	}
	shapeConfig := core.UpdateInstanceShapeConfigDetails{}
	if ocpus != nil && *ocpus > 0 {
		shapeConfig.Ocpus = ocpus
	}
	if memoryInGBs != nil && *memoryInGBs > 0 {
		shapeConfig.MemoryInGBs = memoryInGBs
	}
	updateInstanceDetails.ShapeConfig = &shapeConfig

	// Oracle Cloud Agent 配置
	if disable != nil && details != nil {
		for i := 0; i < len(details); i++ {
			if *disable {
				details[i].DesiredState = core.InstanceAgentPluginConfigDetailsDesiredStateDisabled
			} else {
				details[i].DesiredState = core.InstanceAgentPluginConfigDetailsDesiredStateEnabled
			}
		}
		agentConfig := core.UpdateInstanceAgentConfigDetails{
			IsMonitoringDisabled:  disable, // 是否禁用监控插件
			IsManagementDisabled:  disable, // 是否禁用管理插件
			AreAllPluginsDisabled: disable, // 是否禁用所有可用的插件（管理和监控插件）
			PluginsConfig:         details,
		}
		updateInstanceDetails.AgentConfig = &agentConfig
	}

	req := core.UpdateInstanceRequest{
		InstanceId:            instanceId,
		UpdateInstanceDetails: updateInstanceDetails,
		RequestMetadata:       getCustomRequestMetadataWithRetryPolicy(),
	}
	return c.UpdateInstance(ctx, req)
}
func instanceAction(c core.ComputeClient, instanceId *string, action core.InstanceActionActionEnum) (ins core.Instance, err error) {
	req := core.InstanceActionRequest{
		InstanceId:      instanceId,
		Action:          action,
		RequestMetadata: getCustomRequestMetadataWithRetryPolicy(),
	}
	resp, err := c.InstanceAction(ctx, req)
	ins = resp.Instance
	return
}
func changePublicIp(clients *OciClients, vnics []core.Vnic) (publicIp core.PublicIp, err error) {
	var vnic core.Vnic
	for _, v := range vnics {
		if *v.IsPrimary {
			vnic = v
		}
	}
	fmt.Println("正在获取私有IP...")
	var privateIps []core.PrivateIp
	privateIps, err = getPrivateIps(clients.Network, vnic.Id)
	if err != nil {
		printlnErr("获取私有IP失败", err.Error())
		return
	}
	var privateIp core.PrivateIp
	for _, p := range privateIps {
		if *p.IsPrimary {
			privateIp = p
		}
	}

	fmt.Println("正在获取公共IP OCID...")
	publicIp, err = getPublicIp(clients.Network, privateIp.Id)
	if err != nil {
		printlnErr("获取公共IP OCID 失败", err.Error())
	}
	fmt.Println("正在删除公共IP...")
	_, err = deletePublicIp(clients.Network, publicIp.Id)
	if err != nil {
		printlnErr("删除公共IP 失败", err.Error())
	}
	time.Sleep(3 * time.Second)
	fmt.Println("正在创建公共IP...")
	tenancyId, _ := clients.Provider.TenancyOCID()
	publicIp, err = createPublicIp(clients.Network, privateIp.Id, &tenancyId)
	return
}
func getInstanceVnics(clients *OciClients, instanceId *string) (vnics []core.Vnic, err error) {
	tenancyId, _ := clients.Provider.TenancyOCID()
	vnicAttachments, _, err := ListVnicAttachments(ctx, clients.Compute, &tenancyId, instanceId, nil)
	if err != nil {
		return
	}
	for _, vnicAttachment := range vnicAttachments {
		vnic, vnicErr := GetVnic(clients.Network, vnicAttachment.VnicId)
		if vnicErr != nil {
			printf("GetVnic 错误: %s\n", vnicErr.Error())
			continue
		}
		vnics = append(vnics, vnic)
	}
	return
}
func getPrivateIps(c core.VirtualNetworkClient, vnicId *string) ([]core.PrivateIp, error) {
	req := core.ListPrivateIpsRequest{
		VnicId:          vnicId,
		RequestMetadata: getCustomRequestMetadataWithRetryPolicy(),
	}
	resp, err := c.ListPrivateIps(ctx, req)
	if err == nil && (resp.Items == nil || len(resp.Items) == 0) {
		err = errors.New("私有IP为空")
	}
	return resp.Items, err
}
func getPublicIp(c core.VirtualNetworkClient, privateIpId *string) (core.PublicIp, error) {
	req := core.GetPublicIpByPrivateIpIdRequest{
		GetPublicIpByPrivateIpIdDetails: core.GetPublicIpByPrivateIpIdDetails{PrivateIpId: privateIpId},
		RequestMetadata:                 getCustomRequestMetadataWithRetryPolicy(),
	}
	resp, err := c.GetPublicIpByPrivateIpId(ctx, req)
	if err == nil && resp.PublicIp.Id == nil {
		err = errors.New("未分配公共IP")
	}
	return resp.PublicIp, err
}
func deletePublicIp(c core.VirtualNetworkClient, publicIpId *string) (core.DeletePublicIpResponse, error) {
	req := core.DeletePublicIpRequest{
		PublicIpId:      publicIpId,
		RequestMetadata: getCustomRequestMetadataWithRetryPolicy()}
	return c.DeletePublicIp(ctx, req)
}
func createPublicIp(c core.VirtualNetworkClient, privateIpId *string, tenancyId *string) (core.PublicIp, error) {
	var publicIp core.PublicIp
	req := core.CreatePublicIpRequest{
		CreatePublicIpDetails: core.CreatePublicIpDetails{
			CompartmentId: tenancyId,
			Lifetime:      core.CreatePublicIpDetailsLifetimeEphemeral,
			PrivateIpId:   privateIpId,
		},
		RequestMetadata: getCustomRequestMetadataWithRetryPolicy(),
	}
	resp, err := c.CreatePublicIp(ctx, req)
	publicIp = resp.PublicIp
	return publicIp, err
}
func getInstancePublicIps(clients *OciClients, instanceId *string) (ips []string, err error) {
	// 多次尝试，避免刚抢购到实例，实例正在预配获取不到公共IP。
	var ins core.Instance
	for i := 0; i < 100; i++ {
		if ins.LifecycleState != core.InstanceLifecycleStateRunning {
			ins, err = getInstance(clients.Compute, instanceId)
			if err != nil {
				continue
			}
			if ins.LifecycleState == core.InstanceLifecycleStateTerminating || ins.LifecycleState == core.InstanceLifecycleStateTerminated {
				err = errors.New("实例已终止?")
				return
			}
		}

		tenancyId, _ := clients.Provider.TenancyOCID()
		var vnicAttachments []core.VnicAttachment
		vnicAttachments, _, err = ListVnicAttachments(ctx, clients.Compute, &tenancyId, instanceId, nil)
		if err != nil {
			continue
		}
		if len(vnicAttachments) > 0 {
			for _, vnicAttachment := range vnicAttachments {
				vnic, vnicErr := GetVnic(clients.Network, vnicAttachment.VnicId)
				if vnicErr != nil {
					printf("GetVnic 错误: %s\n", vnicErr.Error())
					continue
				}
				if vnic.PublicIp != nil && *vnic.PublicIp != "" {
					ips = append(ips, *vnic.PublicIp)
				}
			}
			if len(ips) > 0 {
				return
			}
		}
		time.Sleep(3 * time.Second)
	}
	return
}
func getBootVolumes(c core.BlockstorageClient, availabilityDomain, tenancyId *string) ([]core.BootVolume, error) {
	req := core.ListBootVolumesRequest{
		AvailabilityDomain: availabilityDomain,
		CompartmentId:      tenancyId,
		RequestMetadata:    getCustomRequestMetadataWithRetryPolicy(),
	}
	resp, err := c.ListBootVolumes(ctx, req)
	return resp.Items, err
}
func getBootVolume(c core.BlockstorageClient, bootVolumeId *string) (core.BootVolume, error) {
	req := core.GetBootVolumeRequest{
		BootVolumeId:    bootVolumeId,
		RequestMetadata: getCustomRequestMetadataWithRetryPolicy(),
	}
	resp, err := c.GetBootVolume(ctx, req)
	return resp.BootVolume, err
}
func updateBootVolume(c core.BlockstorageClient, bootVolumeId *string, sizeInGBs *int64, vpusPerGB *int64) (core.BootVolume, error) {
	updateBootVolumeDetails := core.UpdateBootVolumeDetails{}
	if sizeInGBs != nil {
		updateBootVolumeDetails.SizeInGBs = sizeInGBs
	}
	if vpusPerGB != nil {
		updateBootVolumeDetails.VpusPerGB = vpusPerGB
	}
	req := core.UpdateBootVolumeRequest{
		BootVolumeId:            bootVolumeId,
		UpdateBootVolumeDetails: updateBootVolumeDetails,
		RequestMetadata:         getCustomRequestMetadataWithRetryPolicy(),
	}
	resp, err := c.UpdateBootVolume(ctx, req)
	return resp.BootVolume, err
}
func deleteBootVolume(c core.BlockstorageClient, bootVolumeId *string) (*http.Response, error) {
	req := core.DeleteBootVolumeRequest{
		BootVolumeId:    bootVolumeId,
		RequestMetadata: getCustomRequestMetadataWithRetryPolicy(),
	}
	resp, err := c.DeleteBootVolume(ctx, req)
	return resp.RawResponse, err
}
func detachBootVolume(c core.ComputeClient, bootVolumeAttachmentId *string) (*http.Response, error) {
	req := core.DetachBootVolumeRequest{
		BootVolumeAttachmentId: bootVolumeAttachmentId,
		RequestMetadata:        getCustomRequestMetadataWithRetryPolicy(),
	}
	resp, err := c.DetachBootVolume(ctx, req)
	return resp.RawResponse, err
}
func listBootVolumeAttachments(c core.ComputeClient, availabilityDomain, compartmentId, bootVolumeId *string) ([]core.BootVolumeAttachment, error) {
	req := core.ListBootVolumeAttachmentsRequest{
		AvailabilityDomain: availabilityDomain,
		CompartmentId:      compartmentId,
		BootVolumeId:       bootVolumeId,
		RequestMetadata:    getCustomRequestMetadataWithRetryPolicy(),
	}
	resp, err := c.ListBootVolumeAttachments(ctx, req)
	return resp.Items, err
}
func sendMessage(chatId, name, text string, keyboard *InlineKeyboardMarkup) (msg Message, err error) {
	if appConfig.token == "" || chatId == "" {
		return Message{}, errors.New("token或chat_id为空")
	}

	data := url.Values{
		"parse_mode": {"Markdown"},
		"chat_id":    {chatId},
		"text":       {"🔰*甲骨文通知* " + name + "\n" + text},
	}

	if keyboard != nil {
		keyboardBytes, _ := json.Marshal(keyboard)
		data.Set("reply_markup", string(keyboardBytes))
	}

	req, err := http.NewRequest(http.MethodPost, appConfig.sendMessageUrl, strings.NewReader(data.Encode()))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := getTgClient()
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var body []byte
	body, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		return
	}
	err = json.Unmarshal(body, &msg)
	if err != nil {
		return
	}
	if !msg.OK {
		err = errors.New(msg.Description)
		return
	}

	return
}
func editMessage(messageId int, chatId, name, text string, keyboard *InlineKeyboardMarkup) (msg Message, err error) {
	if appConfig.token == "" || chatId == "" {
		if messageId == 0 {
			return sendMessage(chatId, name, text, keyboard)
		}
		return Message{}, errors.New("token或chat_id为空")
	}

	data := url.Values{
		"parse_mode": {"Markdown"},
		"chat_id":    {chatId},
		"message_id": {strconv.Itoa(messageId)},
		"text":       {"🔰*甲骨文通知* " + name + "\n" + text},
	}

	if keyboard != nil {
		keyboardBytes, _ := json.Marshal(keyboard)
		data.Set("reply_markup", string(keyboardBytes))
	} else {
		// 发送一个空的keyboard来移除按钮
		emptyKeyboard := InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{}}
		keyboardBytes, _ := json.Marshal(emptyKeyboard)
		data.Set("reply_markup", string(keyboardBytes))
	}

	req, err := http.NewRequest(http.MethodPost, appConfig.editMessageUrl, strings.NewReader(data.Encode()))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := getTgClient()
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var body []byte
	body, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		return
	}
	err = json.Unmarshal(body, &msg)
	if err != nil {
		return
	}
	if !msg.OK {
		// 忽略 "message is not modified" 错误, 因为这很常见
		if !strings.Contains(msg.Description, "message is not modified") {
			err = errors.New(msg.Description)
		} else {
			err = nil // 将其视为无错误
		}
		return
	}

	return
}

// getOciHttpClient 创建一个带有代理和超时设置的http客户端
func getOciHttpClient() *http.Client {
	client := &http.Client{
		Timeout: 30 * time.Second, // 增加30秒超时
	}
	if appConfig.proxy != "" {
		proxyURL, err := url.Parse(appConfig.proxy)
		if err != nil {
			printlnErr("代理URL解析失败", err.Error())
		} else {
			fmt.Println("正在使用代理:", appConfig.proxy)
			client.Transport = &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
			}
		}
	}
	return client
}

func getInstanceState(state core.InstanceLifecycleStateEnum) string {
	var friendlyState string
	switch state {
	case core.InstanceLifecycleStateMoving:
		friendlyState = "正在移动"
	case core.InstanceLifecycleStateProvisioning:
		friendlyState = "正在预配"
	case core.InstanceLifecycleStateRunning:
		friendlyState = "正在运行"
	case core.InstanceLifecycleStateStarting:
		friendlyState = "正在启动"
	case core.InstanceLifecycleStateStopping:
		friendlyState = "正在停止"
	case core.InstanceLifecycleStateStopped:
		friendlyState = "已停止　"
	case core.InstanceLifecycleStateTerminating:
		friendlyState = "正在终止"
	case core.InstanceLifecycleStateTerminated:
		friendlyState = "已终止　"
	default:
		friendlyState = string(state)
	}
	return friendlyState
}
func getBootVolumeState(state core.BootVolumeLifecycleStateEnum) string {
	var friendlyState string
	switch state {
	case core.BootVolumeLifecycleStateProvisioning:
		friendlyState = "正在预配"
	case core.BootVolumeLifecycleStateRestoring:
		friendlyState = "正在恢复"
	case core.BootVolumeLifecycleStateAvailable:
		friendlyState = "可用　　"
	case core.BootVolumeLifecycleStateTerminating:
		friendlyState = "正在终止"
	case core.BootVolumeLifecycleStateTerminated:
		friendlyState = "已终止　"
	case core.BootVolumeLifecycleStateFaulty:
		friendlyState = "故障　　"
	default:
		friendlyState = string(state)
	}
	return friendlyState
}
func fmtDuration(d time.Duration) string {
	if d.Seconds() < 1 {
		return "< 1 秒"
	}
	var buffer bytes.Buffer

	days := int(d / (time.Hour * 24))
	hours := int((d % (time.Hour * 24)).Hours())
	minutes := int((d % time.Hour).Minutes())
	seconds := int((d % time.Minute).Seconds())

	if days > 0 {
		buffer.WriteString(fmt.Sprintf("%d 天 ", days))
	}
	if hours > 0 {
		buffer.WriteString(fmt.Sprintf("%d 时 ", hours))
	}
	if minutes > 0 {
		buffer.WriteString(fmt.Sprintf("%d 分 ", minutes))
	}
	if seconds > 0 {
		buffer.WriteString(fmt.Sprintf("%d 秒", seconds))
	}
	return buffer.String()
}
func printf(format string, a ...interface{}) {
	fmt.Printf("%s ", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Printf(format, a...)
}
func printlnErr(desc, detail string) {
	fmt.Printf("\033[1;31m错误: %s. %s\033[0m\n", desc, detail)
}
func getCustomRequestMetadataWithRetryPolicy() common.RequestMetadata {
	return common.RequestMetadata{
		RetryPolicy: getCustomRetryPolicy(),
	}
}
func getCustomRetryPolicy() *common.RetryPolicy {
	// 重试次数
	attempts := uint(3)
	// 对所有非200状态码进行重试
	retryOnAllNon200ResponseCodes := func(r common.OCIOperationResponse) bool {
		return !(r.Error == nil && 199 < r.Response.HTTPResponse().StatusCode && r.Response.HTTPResponse().StatusCode < 300)
	}
	policy := common.NewRetryPolicyWithOptions(
		common.WithMaximumNumberAttempts(attempts),
		common.WithShouldRetryOperation(retryOnAllNon200ResponseCodes))
	return &policy
}
func command(cmd string) {
	res := strings.Fields(cmd)
	if len(res) > 0 {
		fmt.Println("执行命令:", strings.Join(res, " "))
		name := res[0]
		arg := res[1:]
		out, err := exec.Command(name, arg...).CombinedOutput()
		if err == nil {
			fmt.Println(string(out))
		} else {
			fmt.Println(err)
		}
	}
}
func (app *App) manageAdmins() {
	for {
		fmt.Printf("\n\033[1;32m管理员管理\033[0m \n(当前账号: %s)\n\n", app.oracleSectionName)
		fmt.Println("1. 查看管理员列表")
		fmt.Println("2. 新增管理员")
		fmt.Print("请输入序号 (输入 'q' 或直接回车返回): ")

		var input string
		fmt.Scanln(&input)
		if input == "" || strings.EqualFold(input, "q") {
			return
		}

		num, _ := strconv.Atoi(input)
		switch num {
		case 1:
			app.listAdmins()
		case 2:
			app.createAdmin()
		default:
			fmt.Println("\033[1;31m输入无效\033[0m")
		}
	}
}
func (app *App) listAdmins() {
	fmt.Println("正在获取用户列表...")
	req := identity.ListUsersRequest{CompartmentId: &app.oracleConfig.Tenancy}
	resp, err := app.clients.Identity.ListUsers(ctx, req)
	if err != nil {
		printlnErr("获取用户列表失败", err.Error())
		return
	}

	if len(resp.Items) == 0 {
		fmt.Println("没有找到任何用户。")
		return
	}

	fmt.Printf("\n\033[1;32m管理员列表\033[0m\n")
	w := new(tabwriter.Writer)
	w.Init(os.Stdout, 0, 8, 2, '\t', 0)
	fmt.Fprintln(w, "序号\t名称\t邮箱\t状态")
	for i, user := range resp.Items {
		name := "N/A"
		if user.Name != nil {
			name = *user.Name
		}
		email := "N/A"
		if user.Email != nil {
			email = *user.Email
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", i+1, name, email, user.LifecycleState)
	}
	w.Flush()

	fmt.Print("\n请输入要操作的管理员序号 (输入 'q' 或直接回车返回): ")
	var input string
	fmt.Scanln(&input)
	if input == "" || strings.EqualFold(input, "q") {
		return
	}

	index, err := strconv.Atoi(input)
	if err != nil || index < 1 || index > len(resp.Items) {
		fmt.Println("\033[1;31m无效的序号\033[0m")
		return
	}

	app.adminDetails(resp.Items[index-1])
}
func (app *App) adminDetails(user identity.User) {
	for {
		name := "N/A"
		if user.Name != nil {
			name = *user.Name
		}
		description := "N/A"
		if user.Description != nil {
			description = *user.Description
		}
		email := "N/A"
		if user.Email != nil {
			email = *user.Email
		}

		fmt.Printf("\n\033[1;32m管理员详细信息: %s\033[0m\n", name)
		w := new(tabwriter.Writer)
		w.Init(os.Stdout, 0, 8, 2, '\t', 0)
		fmt.Fprintf(w, "ID:\t%s\n", *user.Id)
		fmt.Fprintf(w, "描述:\t%s\n", description)
		fmt.Fprintf(w, "邮箱:\t%s\n", email)
		fmt.Fprintf(w, "创建时间:\t%s\n", user.TimeCreated.Format(timeLayout))
		fmt.Fprintf(w, "状态:\t%s\n", user.LifecycleState)
		w.Flush()

		fmt.Println("\n1. 修改信息")
		fmt.Println("2. 删除管理员")
		fmt.Print("请输入序号 (输入 'q' 或直接回车返回): ")

		var input string
		fmt.Scanln(&input)
		if input == "" || strings.EqualFold(input, "q") {
			return
		}

		num, _ := strconv.Atoi(input)
		switch num {
		case 1:
			app.updateAdmin(user)
			return // 返回以刷新详情
		case 2:
			app.deleteAdmin(user)
			return // 返回到管理员列表
		default:
			fmt.Println("\033[1;31m输入无效\033[0m")
		}
	}
}
func (app *App) createAdmin() {
	var name, description, email string
	fmt.Print("请输入新管理员用户名 (必须是邮箱格式): ")
	fmt.Scanln(&name)
	fmt.Print("请输入新管理员描述: ")
	fmt.Scanln(&description)
	email = name // 在OCI中，用户名就是邮箱

	req := identity.CreateUserRequest{
		CreateUserDetails: identity.CreateUserDetails{
			CompartmentId: &app.oracleConfig.Tenancy,
			Name:          &name,
			Description:   &description,
			Email:         &email,
		},
	}

	fmt.Println("正在创建用户...")
	userResp, err := app.clients.Identity.CreateUser(ctx, req)
	if err != nil {
		printlnErr("创建用户失败", err.Error())
		return
	}
	fmt.Printf("\033[1;32m用户 '%s' 创建成功！\033[0m\n", *userResp.User.Name)

	fmt.Println("正在将用户添加到 'Administrators' 组...")
	// 查找Administrators组
	listGroupsResp, err := app.clients.Identity.ListGroups(ctx, identity.ListGroupsRequest{CompartmentId: &app.oracleConfig.Tenancy, Name: common.String("Administrators")})
	if err != nil || len(listGroupsResp.Items) == 0 {
		printlnErr("找不到 'Administrators' 组", "")
		return
	}
	adminGroup := listGroupsResp.Items[0]

	addUserReq := identity.AddUserToGroupRequest{
		AddUserToGroupDetails: identity.AddUserToGroupDetails{
			UserId:  userResp.User.Id,
			GroupId: adminGroup.Id,
		},
	}
	_, err = app.clients.Identity.AddUserToGroup(ctx, addUserReq)
	if err != nil {
		printlnErr("添加用户到 'Administrators' 组失败", err.Error())
		return
	}

	fmt.Printf("\033[1;32m成功将用户 '%s' 添加到 'Administrators' 组，已赋予完全管理权限。\033[0m\n", *userResp.User.Name)
	fmt.Println("用户需要检查邮箱并设置密码才能登录。")
}
func (app *App) updateAdmin(user identity.User) {
	var description, email string

	currentDesc := "N/A"
	if user.Description != nil {
		currentDesc = *user.Description
	}
	fmt.Printf("请输入新的描述 (当前: %s, 直接回车不修改): ", currentDesc)
	fmt.Scanln(&description)
	if description == "" {
		description = currentDesc
	}

	currentEmail := "N/A"
	if user.Email != nil {
		currentEmail = *user.Email
	}
	fmt.Printf("请输入新的邮箱 (当前: %s, 直接回车不修改): ", currentEmail)
	fmt.Scanln(&email)
	if email == "" {
		email = currentEmail
	}

	req := identity.UpdateUserRequest{
		UserId: user.Id,
		UpdateUserDetails: identity.UpdateUserDetails{
			Description: &description,
			Email:       &email,
		},
	}

	fmt.Println("正在更新用户信息...")
	_, err := app.clients.Identity.UpdateUser(ctx, req)
	if err != nil {
		printlnErr("更新用户信息失败", err.Error())
		return
	}
	fmt.Println("\033[1;32m用户信息更新成功！\033[0m")
}
func (app *App) deleteAdmin(user identity.User) {
	userName := "N/A"
	if user.Name != nil {
		userName = *user.Name
	}
	fmt.Printf("确定要删除管理员 '%s' 吗？这是一个不可逆的操作！(输入 y 并回车): ", userName)
	var confirmInput string
	fmt.Scanln(&confirmInput)
	if !strings.EqualFold(confirmInput, "y") {
		fmt.Println("操作已取消。")
		return
	}

	req := identity.DeleteUserRequest{UserId: user.Id}
	fmt.Println("正在删除用户...")
	_, err := app.clients.Identity.DeleteUser(ctx, req)
	if err != nil {
		printlnErr("删除用户失败", err.Error())
		return
	}
	fmt.Printf("\033[1;32m用户 '%s' 已成功删除。\033[0m\n", userName)
}
func (app *App) manageNetwork() {
	for {
		fmt.Printf("\n\033[1;32m网络管理\033[0m \n(当前账号: %s)\n\n", app.oracleSectionName)
		fmt.Println("1. 查看子网")
		fmt.Println("2. 查看防火墙 (安全列表)")
		fmt.Print("请输入序号 (输入 'q' 或直接回车返回): ")

		var input string
		fmt.Scanln(&input)
		if input == "" || strings.EqualFold(input, "q") {
			return
		}

		num, _ := strconv.Atoi(input)
		switch num {
		case 1:
			app.listAllSubnets()
		case 2:
			app.listAllSecurityLists()
		default:
			fmt.Println("\033[1;31m输入无效\033[0m")
		}
	}
}
func (app *App) listAllSubnets() {
	fmt.Println("正在获取子网列表...")
	subnets, err := listSubnets(ctx, app.clients.Network, nil, &app.oracleConfig.Tenancy) // nil VCN ID to list all
	if err != nil {
		printlnErr("获取子网列表失败", err.Error())
		return
	}
	if len(subnets) == 0 {
		fmt.Println("没有找到子网。")
		return
	}
	fmt.Printf("\n\033[1;32m子网列表\033[0m\n")
	w := new(tabwriter.Writer)
	w.Init(os.Stdout, 0, 8, 2, '\t', 0)
	fmt.Fprintln(w, "序号\t名称\tCIDR\tIPv6 CIDR\t状态\t类型")
	for i, subnet := range subnets {
		subnetType := "区域性"
		if subnet.AvailabilityDomain != nil && *subnet.AvailabilityDomain != "" {
			subnetType = "AD特定"
		}
		ipv6Cidr := "N/A"
		if subnet.Ipv6CidrBlock != nil {
			ipv6Cidr = *subnet.Ipv6CidrBlock
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", i+1, *subnet.DisplayName, *subnet.CidrBlock, ipv6Cidr, subnet.LifecycleState, subnetType)
	}
	w.Flush()
	// TODO: 如果需要，添加修改逻辑
}
func (app *App) listAllSecurityLists() {
	fmt.Println("正在获取安全列表...")
	req := core.ListSecurityListsRequest{CompartmentId: &app.oracleConfig.Tenancy}
	resp, err := app.clients.Network.ListSecurityLists(ctx, req)
	if err != nil {
		printlnErr("获取安全列表失败", err.Error())
		return
	}
	if len(resp.Items) == 0 {
		fmt.Println("没有找到安全列表。")
		return
	}

	fmt.Printf("\n\033[1;32m安全列表 (防火墙)\033[0m\n")
	w := new(tabwriter.Writer)
	w.Init(os.Stdout, 0, 8, 2, '\t', 0)
	fmt.Fprintln(w, "序号\t名称\t状态\t关联VCN")
	for i, sl := range resp.Items {
		vcn, err := getVcn(app.clients.Network, sl.VcnId)
		vcnName := *sl.VcnId
		if err == nil {
			vcnName = *vcn.DisplayName
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", i+1, *sl.DisplayName, sl.LifecycleState, vcnName)
	}
	w.Flush()

	fmt.Print("\n请输入要查看规则的序号 (输入 'q' 或直接回车返回): ")
	var input string
	fmt.Scanln(&input)
	if input == "" || strings.EqualFold(input, "q") {
		return
	}

	index, err := strconv.Atoi(input)
	if err != nil || index < 1 || index > len(resp.Items) {
		fmt.Println("\033[1;31m无效的序号\033[0m")
		return
	}
	app.securityListDetails(resp.Items[index-1])
}
func (app *App) securityListDetails(sl core.SecurityList) {
	fmt.Printf("\n\033[1;32m入站规则 for %s\033[0m\n", *sl.DisplayName)
	iw := new(tabwriter.Writer)
	iw.Init(os.Stdout, 0, 8, 2, '\t', 0)
	fmt.Fprintln(iw, "源\t协议\t有状态\t描述")
	for _, rule := range sl.IngressSecurityRules {
		fmt.Fprintf(iw, "%s\t%s\t%t\t%s\n", *rule.Source, *rule.Protocol, *rule.IsStateless, rule.Description)
	}
	iw.Flush()

	fmt.Printf("\n\033[1;32m出站规则 for %s\033[0m\n", *sl.DisplayName)
	ew := new(tabwriter.Writer)
	ew.Init(os.Stdout, 0, 8, 2, '\t', 0)
	fmt.Fprintln(ew, "目标\t协议\t有状态\t描述")
	for _, rule := range sl.EgressSecurityRules {
		fmt.Fprintf(ew, "%s\t%s\t%t\t%s\n", *rule.Destination, *rule.Protocol, *rule.IsStateless, rule.Description)
	}
	ew.Flush()
	// TODO: 添加规则修改逻辑
}
func getSubnet(c core.VirtualNetworkClient, subnetId *string) (core.Subnet, error) {
	req := core.GetSubnetRequest{SubnetId: subnetId}
	resp, err := c.GetSubnet(ctx, req)
	return resp.Subnet, err
}
func getVcn(c core.VirtualNetworkClient, vcnId *string) (core.Vcn, error) {
	req := core.GetVcnRequest{VcnId: vcnId}
	resp, err := c.GetVcn(ctx, req)
	return resp.Vcn, err
}
func (app *App) viewInstanceTraffic(instanceId *string) {
	for {
		fmt.Printf("\n\033[1;32m查看实例流量\033[0m\n")
		fmt.Println("1. 最近24小时")
		fmt.Println("2. 最近7天")
		fmt.Println("3. 自定义时间范围")
		fmt.Print("请输入序号 (输入 'q' 或直接回车返回): ")

		var input string
		fmt.Scanln(&input)
		if input == "" || strings.EqualFold(input, "q") {
			return
		}

		num, _ := strconv.Atoi(input)
		endTime := time.Now().UTC()
		var startTime time.Time
		var resolution = "1h"

		switch num {
		case 1:
			startTime = endTime.Add(-24 * time.Hour)
		case 2:
			startTime = endTime.Add(-7 * 24 * time.Hour)
			resolution = "1d"
		case 3:
			fmt.Print("请输入开始时间 (格式 YYYY-MM-DD): ")
			var startStr string
			fmt.Scanln(&startStr)
			st, err := time.Parse("2006-01-02", startStr)
			if err != nil {
				fmt.Println("时间格式错误。")
				continue
			}
			startTime = st.UTC()

			fmt.Print("请输入结束时间 (格式 YYYY-MM-DD, 默认为现在): ")
			var endStr string
			fmt.Scanln(&endStr)
			if endStr != "" {
				et, err := time.Parse("2006-01-02", endStr)
				if err != nil {
					fmt.Println("时间格式错误。")
					continue
				}
				endTime = et.UTC()
			}
		default:
			fmt.Println("无效输入。")
			continue
		}

		app.queryTraffic(instanceId, startTime, endTime, resolution)
	}
}
func (app *App) queryTraffic(instanceId *string, startTime, endTime time.Time, resolution string) {
	fmt.Println("正在查询流量数据，请稍候...")
	namespace := "oci_computeagent"
	// 指标: NetworksBytesIn, NetworksBytesOut
	queryIn := fmt.Sprintf("NetworksBytesIn[1m]{resourceId = \"%s\"}.sum()", *instanceId)
	queryOut := fmt.Sprintf("NetworksBytesOut[1m]{resourceId = \"%s\"}.sum()", *instanceId)

	var totalIn, totalOut float64

	// 获取入站流量
	inResp, err := getMetrics(app.clients.Monitoring, &app.oracleConfig.Tenancy, namespace, queryIn, startTime, endTime, resolution)
	if err != nil {
		printlnErr("获取入站流量失败", err.Error())
	} else if len(inResp.Items) > 0 {
		for _, dp := range inResp.Items[0].AggregatedDatapoints {
			totalIn += *dp.Value
		}
	}

	// 获取出站流量
	outResp, err := getMetrics(app.clients.Monitoring, &app.oracleConfig.Tenancy, namespace, queryOut, startTime, endTime, resolution)
	if err != nil {
		printlnErr("获取出站流量失败", err.Error())
	} else if len(outResp.Items) > 0 {
		for _, dp := range outResp.Items[0].AggregatedDatapoints {
			totalOut += *dp.Value
		}
	}

	fmt.Printf("\n\033[1;32m流量使用情况 (%s to %s)\033[0m\n", startTime.Format(timeLayout), endTime.Format(timeLayout))
	fmt.Printf("总入站流量 (Downloaded): %s\n", formatBytes(totalIn))
	fmt.Printf("总出站流量 (Uploaded):   %s\n", formatBytes(totalOut))
	fmt.Printf("总计使用流量:           %s\n", formatBytes(totalIn+totalOut))
}
func getMetrics(c monitoring.MonitoringClient, tenancyId *string, namespace, query string, startTime, endTime time.Time, resolution string) (monitoring.SummarizeMetricsDataResponse, error) {
	req := monitoring.SummarizeMetricsDataRequest{
		CompartmentId: tenancyId,
		SummarizeMetricsDataDetails: monitoring.SummarizeMetricsDataDetails{
			Namespace:  &namespace,
			Query:      &query,
			StartTime:  &common.SDKTime{Time: startTime},
			EndTime:    &common.SDKTime{Time: endTime},
			Resolution: &resolution,
		},
	}
	return c.SummarizeMetricsData(ctx, req)
}
func formatBytes(b float64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%.2f B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", b/float64(div), "KMGTPE"[exp])
}
func (app *App) manageTenantAndUser() {
	for {
		fmt.Printf("\n\033[1;32m租户与用户信息\033[0m\n")
		fmt.Println("1. 查看租户详细信息")
		fmt.Println("2. 修改我的恢复邮箱")
		fmt.Print("请输入序号 (输入 'q' 或直接回车返回): ")

		var input string
		fmt.Scanln(&input)
		if input == "" || strings.EqualFold(input, "q") {
			return
		}

		num, _ := strconv.Atoi(input)
		switch num {
		case 1:
			app.showTenantDetails()
		case 2:
			app.updateMyRecoveryEmail()
		default:
			fmt.Println("\033[1;31m输入无效\033[0m")
		}
	}
}
func (app *App) showTenantDetails() {
	fmt.Println("正在获取租户信息...")
	req := identity.GetTenancyRequest{TenancyId: &app.oracleConfig.Tenancy}
	resp, err := app.clients.Identity.GetTenancy(ctx, req)
	if err != nil {
		printlnErr("获取租户信息失败", err.Error())
		return
	}

	fmt.Printf("\n\033[1;32m租户详细信息\033[0m\n")
	w := new(tabwriter.Writer)
	w.Init(os.Stdout, 0, 8, 2, '\t', 0)
	fmt.Fprintf(w, "名称:\t%s\n", *resp.Tenancy.Name)
	fmt.Fprintf(w, "ID:\t%s\n", *resp.Tenancy.Id)
	fmt.Fprintf(w, "主区域:\t%s\n", *resp.Tenancy.HomeRegionKey)
	if resp.Tenancy.Description != nil {
		fmt.Fprintf(w, "描述:\t%s\n", *resp.Tenancy.Description)
	}
	w.Flush()
}
func (app *App) updateMyRecoveryEmail() {
	userId, err := app.clients.Provider.UserOCID()
	if err != nil {
		printlnErr("无法从配置文件中获取用户ID", err.Error())
		return
	}

	userResp, err := app.clients.Identity.GetUser(ctx, identity.GetUserRequest{UserId: &userId})
	if err != nil {
		printlnErr("获取当前用户信息失败", err.Error())
		return
	}

	var newEmail string
	currentEmail := "N/A"
	if userResp.User.Email != nil {
		currentEmail = *userResp.User.Email
	}
	fmt.Printf("请输入新的恢复邮箱 (当前: %s): ", currentEmail)
	fmt.Scanln(&newEmail)
	if newEmail == "" {
		fmt.Println("操作已取消。")
		return
	}

	req := identity.UpdateUserRequest{
		UserId: &userId,
		UpdateUserDetails: identity.UpdateUserDetails{
			Email: &newEmail,
		},
	}

	fmt.Println("正在更新恢复邮箱...")
	_, err = app.clients.Identity.UpdateUser(ctx, req)
	if err != nil {
		printlnErr("更新恢复邮箱失败", err.Error())
		return
	}
	fmt.Println("\033[1;32m恢复邮箱更新成功！\033[0m")
}
func (app *App) addIpv6ToInstance(vnics []core.Vnic) {
	if len(vnics) == 0 {
		fmt.Printf("\033[1;31m实例已终止或获取实例VNIC失败，请稍后重试.\033[0m\n")
		return
	}

	var primaryVnic core.Vnic
	for _, v := range vnics {
		if *v.IsPrimary {
			primaryVnic = v
			break
		}
	}
	if primaryVnic.Id == nil {
		printlnErr("未找到主网卡", "")
		return
	}

	fmt.Printf("确定要为实例主网卡添加一个IPv6地址吗？(输入 y 并回车): ")
	var confirmInput string
	fmt.Scanln(&confirmInput)
	if !strings.EqualFold(confirmInput, "y") {
		fmt.Println("操作已取消。")
		return
	}

	fmt.Println("正在为网卡添加IPv6地址...")
	req := core.CreateIpv6Request{
		CreateIpv6Details: core.CreateIpv6Details{
			VnicId: primaryVnic.Id,
		},
	}
	resp, err := app.clients.Network.CreateIpv6(ctx, req)
	if err != nil {
		printlnErr("添加IPv6地址失败", err.Error())
		return
	}

	fmt.Printf("\033[1;32m成功为实例添加IPv6地址: %s\033[0m\n", *resp.Ipv6.IpAddress)
	fmt.Println("注意：您可能需要在操作系统内部配置网络以使用此IPv6地址。")
}
func listIpv6s(c core.VirtualNetworkClient, vnicId *string) ([]core.Ipv6, error) {
	req := core.ListIpv6sRequest{VnicId: vnicId}
	resp, err := c.ListIpv6s(ctx, req)
	return resp.Items, err
}
func (app *App) manageTenants() {
	for {
		fmt.Printf("\n\033[1;32m租户管理 (凭证检查)\033[0m \n(当前账号: %s)\n\n", app.oracleSectionName)
		fmt.Println("1. 检查当前租户凭证")
		fmt.Println("2. 一键检查所有租户凭证")
		fmt.Print("请输入序号 (输入 'q' 或直接回车返回): ")

		var input string
		fmt.Scanln(&input)
		if input == "" || strings.EqualFold(input, "q") {
			return
		}

		num, _ := strconv.Atoi(input)
		switch num {
		case 1:
			app.checkCurrentTenantActivity()
		case 2:
			app.checkAllTenantsActivity(false) // false for CLI mode
		default:
			fmt.Println("\033[1;31m输入无效\033[0m")
		}
	}
}
func (app *App) checkCurrentTenantActivity() {
	fmt.Println("正在检查当前租户凭证和活动状态...")
	req := identity.GetTenancyRequest{TenancyId: &app.oracleConfig.Tenancy}
	resp, err := app.clients.Identity.GetTenancy(ctx, req)
	if err != nil {
		printlnErr("租户凭证无效或API调用失败", err.Error())
		fmt.Println("请检查您的 oci-help.ini 配置文件中的 tenancy, user, fingerprint, region 和 key_file 是否正确。")
		return
	}

	fmt.Printf("\n\033[1;32m当前租户凭证有效！\033[0m\n")
	w := new(tabwriter.Writer)
	w.Init(os.Stdout, 0, 8, 2, '\t', 0)
	fmt.Fprintf(w, "租户名称:\t%s\n", *resp.Tenancy.Name)
	fmt.Fprintf(w, "租户ID:\t%s\n", *resp.Tenancy.Id)
	fmt.Fprintf(w, "主区域:\t%s\n", *resp.Tenancy.HomeRegionKey)
	w.Flush()
	fmt.Println("\n按回车键返回...")
	fmt.Scanln()
}

// checkAllTenantsActivity 检查所有已配置的租户并提供摘要。
// 对于botMode，它返回一个格式化的字符串。对于CLI，它会打印到控制台。
func (app *App) checkAllTenantsActivity(botMode bool) string {
	if !botMode {
		fmt.Println("正在一键检查所有租户的凭证...")
	}

	var wg sync.WaitGroup
	resultsChan := make(chan TenantStatus, len(app.oracleSections))

	for _, section := range app.oracleSections {
		wg.Add(1)
		go func(sec *ini.Section) {
			defer wg.Done()

			var oracleConfig Oracle
			err := sec.MapTo(&oracleConfig)
			if err != nil {
				resultsChan <- TenantStatus{Name: sec.Name(), Status: "无效", Message: "配置文件解析失败"}
				return
			}

			provider, err := getProvider(oracleConfig)
			if err != nil {
				resultsChan <- TenantStatus{Name: sec.Name(), Status: "无效", Message: "获取Provider失败: " + err.Error()}
				return
			}
			
			httpClient := getOciHttpClient()
			identityClient, err := identity.NewIdentityClientWithConfigurationProvider(provider)
			if err != nil {
				resultsChan <- TenantStatus{Name: sec.Name(), Status: "无效", Message: "创建IdentityClient失败: " + err.Error()}
				return
			}
			identityClient.HTTPClient = httpClient

			_, err = identityClient.GetTenancy(ctx, identity.GetTenancyRequest{TenancyId: &oracleConfig.Tenancy})
			if err != nil {
				var errMsg string
				if serviceError, ok := common.IsServiceError(err); ok {
					errMsg = fmt.Sprintf("%s (状态码: %d, 服务码: %s)",
						serviceError.GetMessage(),
						serviceError.GetHTTPStatusCode(),
						serviceError.GetCode())
				} else {
					errMsg = err.Error()
				}
				resultsChan <- TenantStatus{Name: sec.Name(), Status: "无效", Message: errMsg}
			} else {
				resultsChan <- TenantStatus{Name: sec.Name(), Status: "有效", Message: "凭证有效"}
			}
		}(section)
	}

	wg.Wait()
	close(resultsChan)

	// 收集和处理结果
	var results []TenantStatus
	for res := range resultsChan {
		results = append(results, res)
	}

	totalCount := len(results)
	normalCount := 0
	var abnormalTenants []TenantStatus

	for _, res := range results {
		if res.Status == "有效" {
			normalCount++
		} else {
			abnormalTenants = append(abnormalTenants, res)
		}
	}
	abnormalCount := len(abnormalTenants)

	// 格式化输出
	var sb strings.Builder
	var summary string

	if botMode {
		sb.WriteString("```\n")
		summary = fmt.Sprintf("检查总数: %d, 正常: %d, 异常: %d", totalCount, normalCount, abnormalCount)
	} else {
		fmt.Printf("\n\033[1;32m所有租户凭证检查结果\033[0m\n")
		summary = fmt.Sprintf("检查总数: %d, 正常: \033[1;32m%d\033[0m, 异常: \033[1;31m%d\033[0m", totalCount, normalCount, abnormalCount)
	}

	sb.WriteString(summary)
	sb.WriteString("\n")

	if abnormalCount > 0 {
		sb.WriteString("\n--- 异常清单 ---\n")
		w := new(tabwriter.Writer)
		w.Init(&sb, 0, 8, 2, ' ', 0)
		fmt.Fprintln(w, "租户名称\t信息")
		fmt.Fprintln(w, "--------\t----")
		for _, res := range abnormalTenants {
			fmt.Fprintf(w, "%s\t%s\n", res.Name, res.Message)
		}
		w.Flush()
	}

	if botMode {
		sb.WriteString("```")
		return sb.String()
	} else {
		fmt.Println(sb.String())
		fmt.Println("\n按回车键返回...")
		fmt.Scanln()
		return ""
	}
}
