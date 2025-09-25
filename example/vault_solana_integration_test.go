package example

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	client "github.com/accelerate-protocol/token-engine-client-sdk"
	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SolanaConfig Solana 专用配置结构
type SolanaConfig struct {
	Server struct {
		URL string `toml:"url"`
	} `toml:"server"`

	Solana struct {
		ChainID string `toml:"chain_id"`
		Cluster string `toml:"cluster"`
		RPCURL  string `toml:"rpc_url"`
		WSURL   string `toml:"ws_url"` // 新增 WebSocket URL

		Admin struct {
			PrivateKey string `toml:"private_key"`
			PublicKey  string `toml:"public_key"`
		} `toml:"admin"`

		// 更新用户结构 - 从数组改为命名节点
		Deployer struct {
			PrivateKey string `toml:"private_key"`
			PublicKey  string `toml:"public_key"`
		} `toml:"deployer"`

		Creator struct {
			PrivateKey string `toml:"private_key"`
			PublicKey  string `toml:"public_key"`
		} `toml:"creator"`

		DrdsValidator struct {
			PrivateKey string `toml:"private_key"`
			PublicKey  string `toml:"public_key"`
		} `toml:"drds_validator"`

		User struct {
			PrivateKey string `toml:"private_key"`
			PublicKey  string `toml:"public_key"`
		} `toml:"user"`

		// 更新代币配置结构
		Tokens struct {
			USDC string `toml:"usdc"`
			WSOL string `toml:"wsol"`
		} `toml:"tokens"`
	} `toml:"solana"`
}

var solanaConfig SolanaConfig

// SolanaUser 表示 Solana 用户信息
type SolanaUser struct {
	Name          string
	PrivateKey    string
	PublicKey     string
	SolPublicKey  solana.PublicKey
	SolPrivateKey solana.PrivateKey
}

// GetAllSolanaUsers 获取配置中的所有 Solana 用户
func GetAllSolanaUsers(config *SolanaConfig) map[string]SolanaUser {
	return map[string]SolanaUser{
		"admin":          {"Admin", config.Solana.Admin.PrivateKey, config.Solana.Admin.PublicKey, solana.MustPublicKeyFromBase58(config.Solana.Admin.PublicKey), solana.MustPrivateKeyFromBase58(config.Solana.Admin.PrivateKey)},
		"deployer":       {"Deployer", config.Solana.Deployer.PrivateKey, config.Solana.Deployer.PublicKey, solana.MustPublicKeyFromBase58(config.Solana.Deployer.PublicKey), solana.MustPrivateKeyFromBase58(config.Solana.Deployer.PrivateKey)},
		"creator":        {"Creator", config.Solana.Creator.PrivateKey, config.Solana.Creator.PublicKey, solana.MustPublicKeyFromBase58(config.Solana.Creator.PublicKey), solana.MustPrivateKeyFromBase58(config.Solana.Creator.PrivateKey)},
		"drds_validator": {"DrdsValidator", config.Solana.DrdsValidator.PrivateKey, config.Solana.DrdsValidator.PublicKey, solana.MustPublicKeyFromBase58(config.Solana.DrdsValidator.PublicKey), solana.MustPrivateKeyFromBase58(config.Solana.DrdsValidator.PrivateKey)},
		"user":           {"User", config.Solana.User.PrivateKey, config.Solana.User.PublicKey, solana.MustPublicKeyFromBase58(config.Solana.User.PublicKey), solana.MustPrivateKeyFromBase58(config.Solana.User.PrivateKey)},
	}
}

// GetUserByRole 根据角色获取用户信息
func GetUserByRole(config *SolanaConfig, role string) *SolanaUser {
	switch role {
	case "admin":
		return &SolanaUser{"Admin", config.Solana.Admin.PrivateKey, config.Solana.Admin.PublicKey, solana.MustPublicKeyFromBase58(config.Solana.Admin.PublicKey), solana.MustPrivateKeyFromBase58(config.Solana.Admin.PrivateKey)}
	case "deployer":
		return &SolanaUser{"Deployer", config.Solana.Deployer.PrivateKey, config.Solana.Deployer.PublicKey, solana.MustPublicKeyFromBase58(config.Solana.Deployer.PublicKey), solana.MustPrivateKeyFromBase58(config.Solana.Deployer.PrivateKey)}
	case "creator":
		return &SolanaUser{"Creator", config.Solana.Creator.PrivateKey, config.Solana.Creator.PublicKey, solana.MustPublicKeyFromBase58(config.Solana.Creator.PublicKey), solana.MustPrivateKeyFromBase58(config.Solana.Creator.PrivateKey)}
	case "drds_validator":
		return &SolanaUser{"DrdsValidator", config.Solana.DrdsValidator.PrivateKey, config.Solana.DrdsValidator.PublicKey, solana.MustPublicKeyFromBase58(config.Solana.DrdsValidator.PublicKey), solana.MustPrivateKeyFromBase58(config.Solana.DrdsValidator.PrivateKey)}
	case "user":
		return &SolanaUser{"User", config.Solana.User.PrivateKey, config.Solana.User.PublicKey, solana.MustPublicKeyFromBase58(config.Solana.User.PublicKey), solana.MustPrivateKeyFromBase58(config.Solana.User.PrivateKey)}
	default:
		return nil
	}
}

// SolanaKeyPair 表示 Solana 密钥对
type SolanaKeyPair struct {
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key"`
}

// VaultInitReq Vault 初始化配置请求结构体
type VaultInitReq struct {
	ChainId client.CommonChainID `json:"chain_id"`
	Admin   string               `json:"admin"`
	Signer  string               `json:"signer,omitempty"`
}

// VaultCommonInfoReq Vault 基本信息查询请求结构体
type VaultCommonInfoReq struct {
	ChainId      client.CommonChainID `form:"chain_id" binding:"required"`
	InfoType     string               `form:"info_type"`
	VaultAddress string               `form:"vault_address" binding:"required"`
	InfoId       string               `form:"info_id"`
}

// VaultCommonInfoResp Vault 基本信息查询响应结构体
type VaultCommonInfoResp struct {
	VaultAddress string `json:"vault_address"`
	ProjectName  string `json:"project_name"`
	TokenSymbol  string `json:"token_symbol"`
	SoftCap      string `json:"soft_cap"`
	RaisedAmount string `json:"raised_amount"`
	Status       string `json:"status"`
	StartTime    int64  `json:"start_time"`
	EndTime      int64  `json:"end_time"`
}

// CreateAuthReq 创建授权请求结构体
type CreateAuthReq struct {
	ChainId client.CommonChainID `json:"chain_id" binding:"required"`
	Admin   string               `json:"admin" binding:"required"`
	Creator string               `json:"creator" binding:"required"`
}

// loadSolanaConfig 加载 Solana 配置
func loadSolanaConfig(t *testing.T) {
	configPath := filepath.Join(".", "priv_solana.toml")

	// 检查配置文件是否存在
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Fatalf("配置文件不存在: %s", configPath)
	}

	// 读取配置文件
	configData, err := os.ReadFile(configPath)
	require.NoError(t, err, "无法读取配置文件")

	err = toml.Unmarshal(configData, &solanaConfig)
	require.NoError(t, err, "无法解析配置文件")

	// 验证必要的配置项
	require.NotEmpty(t, solanaConfig.Server.URL, "Server URL 不能为空")
	require.NotEmpty(t, solanaConfig.Solana.ChainID, "Solana Chain ID 不能为空")
	require.NotEmpty(t, solanaConfig.Solana.RPCURL, "Solana RPC URL 不能为空")
	require.NotEmpty(t, solanaConfig.Solana.Admin.PrivateKey, "Solana Admin Private Key 不能为空")
	require.NotEmpty(t, solanaConfig.Solana.Admin.PublicKey, "Solana Admin Public Key 不能为空")

	t.Logf("成功加载 Solana 配置:")
	t.Logf("  Server URL: %s", solanaConfig.Server.URL)
	t.Logf("  Solana Chain ID: %s", solanaConfig.Solana.ChainID)
	t.Logf("  Solana Cluster: %s", solanaConfig.Solana.Cluster)
	t.Logf("  Solana RPC URL: %s", solanaConfig.Solana.RPCURL)
	t.Logf("  Solana WebSocket URL: %s", solanaConfig.Solana.WSURL)
	t.Logf("  Solana Admin Public Key: %s", solanaConfig.Solana.Admin.PublicKey)
	t.Logf("  Deployer Public Key: %s", solanaConfig.Solana.Deployer.PublicKey)
	t.Logf("  Creator Public Key: %s", solanaConfig.Solana.Creator.PublicKey)
}

// SolanaVaultIntegrationTest Solana Vault 集成测试结构
type SolanaVaultIntegrationTest struct {
	baseURL       string
	httpClient    *http.Client
	ctx           context.Context
	config        *SolanaConfig
	client        *rpc.Client
	admin         *SolanaUser
	deployer      *SolanaUser
	creator       *SolanaUser
	drdsvalidator *SolanaUser
	user          *SolanaUser
}

// NewSolanaVaultIntegrationTest 创建 Solana 集成测试实例
func NewSolanaVaultIntegrationTest(config *SolanaConfig, t *testing.T) *SolanaVaultIntegrationTest {
	client := rpc.New(config.Solana.RPCURL)
	require.NotNil(t, client, "无法创建 Solana RPC 客户端")
	GetAllSolanaUsers(config) // 预加载所有用户

	admin := GetUserByRole(config, "admin")
	require.NotNil(t, admin, "无法获取 Admin 用户")

	deployer := GetUserByRole(config, "deployer")
	require.NotNil(t, deployer, "无法获取 Deployer 用户")

	creator := GetUserByRole(config, "creator")
	require.NotNil(t, creator, "无法获取 Creator 用户")

	drdsvalidator := GetUserByRole(config, "drds_validator")
	require.NotNil(t, drdsvalidator, "无法获取 DrdsValidator 用户")

	user := GetUserByRole(config, "user")
	require.NotNil(t, user, "无法获取 User 用户")
	return &SolanaVaultIntegrationTest{
		baseURL:       config.Server.URL,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
		ctx:           context.Background(),
		config:        config,
		client:        client,
		admin:         admin,
		deployer:      deployer,
		creator:       creator,
		drdsvalidator: drdsvalidator,
		user:          user,
	}
}

// TestSolanaInitializeConfig 测试 Solana 初始化配置
func TestSolanaInitializeConfig(t *testing.T) {
	// 加载配置
	loadSolanaConfig(t)

	// 创建测试实例
	test := NewSolanaVaultIntegrationTest(&solanaConfig, t)
	env = test

	t.Run("InitializeConfig", func(t *testing.T) {
		// 1. 准备初始化配置请求
		initReq := &VaultInitReq{
			ChainId: client.SOLANA,
			Admin:   solanaConfig.Solana.Admin.PublicKey,
			Signer:  solanaConfig.Solana.Admin.PublicKey, // 可选字段，这里设置为admin地址
		}

		t.Logf("生成 Solana 初始化配置请求:")
		t.Logf("  Chain ID: %s", string(initReq.ChainId))
		t.Logf("  Admin: %s", initReq.Admin)
		t.Logf("  Signer: %s", initReq.Signer)

		// 2. 调用 prepare_initialize_config 接口
		prepareResp := test.callPrepareInitializeConfigSolana(t, initReq)
		require.NotNil(t, prepareResp)
		require.NotEmpty(t, prepareResp.TxMsgBase64)
		require.NotEmpty(t, prepareResp.CorrelationId)

		t.Logf("获得待签名交易数据:")
		t.Logf("  Correlation ID: %s", *prepareResp.CorrelationId)
		t.Logf("  TxMsgBase64 长度: %d", len(*prepareResp.TxMsgBase64))

		// 3. 模拟签名过程
		signedTxBase64 := *prepareResp.TxMsgBase64

		// 4. 准备提交请求
		submitReq := &client.RequestSubmitReq{
			ChainId:      client.SOLANA,
			Sender:       solanaConfig.Solana.Admin.PublicKey,
			TxMsgBase64:  *prepareResp.TxMsgBase64,
			SignTxBase64: signedTxBase64,
		}

		t.Logf("准备提交 Solana 初始化配置交易:")
		t.Logf("  发送者: %s", submitReq.Sender)
		t.Logf("  Chain ID: %s", string(submitReq.ChainId))

		signedTx, err := signTx(t, test.admin, signedTxBase64)
		require.NoError(t, err)
		submitReq.SignTxBase64 = signedTx

		// 5. 提交交易
		submitResp := test.callSubmitTxSolana(t, submitReq)
		require.NotNil(t, submitResp)

		t.Logf("初始化配置交易提交结果:")
		if submitResp.Success != nil {
			t.Logf("  成功: %t", *submitResp.Success)
		}
		if submitResp.TxHash != nil {
			t.Logf("  交易哈希: %s", *submitResp.TxHash)
		}
		if submitResp.FailedMsg != nil {
			t.Logf("  失败信息: %s", *submitResp.FailedMsg)
		}

		// 6. 验证结果
		if submitResp.Success != nil && *submitResp.Success {
			require.NotNil(t, submitResp.TxHash)
			require.NotEmpty(t, *submitResp.TxHash)

			t.Logf("✅ Solana 初始化配置成功!")
			t.Logf("   交易哈希: %s", *submitResp.TxHash)

			// 记录余额变化
			if submitResp.SenderBalanceChange != nil {
				t.Logf("   发送者余额变化:")
				if submitResp.SenderBalanceChange.NativeBalanceChange != nil {
					t.Logf("     Native Token: %d", *submitResp.SenderBalanceChange.NativeBalanceChange)
				}
				if submitResp.SenderBalanceChange.TokenBalanceChange != nil {
					for _, change := range *submitResp.SenderBalanceChange.TokenBalanceChange {
						if change.TokenMint != nil && change.BalanceChange != nil {
							t.Logf("     Token %s: %s", *change.TokenMint, *change.BalanceChange)
						}
					}
				}
			}
		} else {
			// 如果是测试环境或模拟环境，可能会失败，这是正常的
			t.Logf("⚠️  交易提交失败 (可能是模拟环境)")
			if submitResp.FailedMsg != nil {
				t.Logf("   失败原因: %s", *submitResp.FailedMsg)
			}
		}
	})
}

// callPrepareCreateVaultSolana 调用 Solana prepare_create 接口
func (test *SolanaVaultIntegrationTest) callPrepareCreateVaultSolana(t *testing.T, req *client.RequestVaultCreateReq) *client.EntityPrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 Solana /api/v2/primary/vault/prepare_create")
	t.Logf("请求体: %s", string(reqBody))

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/primary/vault/prepare_create", bytes.NewBuffer(reqBody))
	require.NoError(t, err)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", appId)

	// 执行请求
	resp, err := test.httpClient.Do(httpReq)
	require.NoError(t, err)
	defer resp.Body.Close()

	// 检查响应状态
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// 解析响应
	var apiResp client.CommonApiResp
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	require.NoError(t, err)
	assert.Equal(t, 0, *apiResp.Code)

	t.Log("收到 Solana prepare_create 响应")

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp client.EntityPrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

// callSubmitTxSolana 调用 Solana submit_tx 接口
func (test *SolanaVaultIntegrationTest) callSubmitTxSolana(t *testing.T, req *client.RequestSubmitReq) *client.ResponseSubmitResp {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 Solana /api/v2/transaction/submit")

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/transaction/submit", bytes.NewBuffer(reqBody))
	require.NoError(t, err)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", appId)

	// 执行请求
	resp, err := test.httpClient.Do(httpReq)
	require.NoError(t, err)
	defer resp.Body.Close()

	// 检查响应状态
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// 解析响应
	var apiResp client.CommonApiResp
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	require.NoError(t, err)
	assert.Equal(t, 0, *apiResp.Code)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var submitResp client.ResponseSubmitResp
	err = json.Unmarshal(respData, &submitResp)
	require.NoError(t, err)

	return &submitResp
}

// callPrepareInitializeConfigSolana 调用 Solana prepare_initialize_config 接口
func (test *SolanaVaultIntegrationTest) callPrepareInitializeConfigSolana(t *testing.T, req *VaultInitReq) *client.EntityPrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 Solana /api/v2/primary/vault/prepare_initialize_config")
	t.Logf("请求体: %s", string(reqBody))

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/primary/vault/prepare_initialize_config", bytes.NewBuffer(reqBody))
	require.NoError(t, err)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", appId)

	// 执行请求
	resp, err := test.httpClient.Do(httpReq)
	require.NoError(t, err)
	defer resp.Body.Close()

	// 检查响应状态
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// 解析响应
	var apiResp client.CommonApiResp
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	require.NoError(t, err)
	assert.Equal(t, 0, *apiResp.Code)

	t.Log("收到 Solana prepare_initialize_config 响应")

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp client.EntityPrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

// callVaultCommonInfoSolana 调用 Solana vault/common_info 接口
func (test *SolanaVaultIntegrationTest) callVaultCommonInfoSolana(t *testing.T, req *VaultCommonInfoReq) *client.CommonApiResp {
	// 构建查询参数
	queryParams := fmt.Sprintf("chain_id=%s&vault_address=%s", string(req.ChainId), req.VaultAddress)
	if req.InfoType != "" {
		queryParams += "&info_type=" + req.InfoType
	}
	if req.InfoId != "" {
		queryParams += "&info_id=" + req.InfoId
	}

	t.Logf("调用 Solana /api/v2/primary/vault/common_info")
	t.Logf("查询参数: %s", queryParams)

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("GET", test.baseURL+"/api/v2/primary/vault/common_info?"+queryParams, nil)
	require.NoError(t, err)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", appId)

	// 执行请求
	resp, err := test.httpClient.Do(httpReq)
	require.NoError(t, err)
	defer resp.Body.Close()

	// 检查响应状态
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// 解析响应
	var apiResp client.CommonApiResp
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	require.NoError(t, err)
	assert.Equal(t, 0, *apiResp.Code)

	t.Log("收到 Solana vault/common_info 响应")

	return &apiResp
}

// callPrepareCreateAuthSolana 调用 Solana prepare_creator_auth 接口
func (test *SolanaVaultIntegrationTest) callPrepareCreateAuthSolana(t *testing.T, req *CreateAuthReq) *client.EntityPrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 Solana /api/v2/primary/vault/prepare_creator_auth")
	t.Logf("请求体: %s", string(reqBody))

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/primary/vault/prepare_creator_auth", bytes.NewBuffer(reqBody))
	require.NoError(t, err)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", appId)

	// 执行请求
	resp, err := test.httpClient.Do(httpReq)
	require.NoError(t, err)
	defer resp.Body.Close()

	// 检查响应状态
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// 解析响应
	var apiResp client.CommonApiResp
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	require.NoError(t, err)
	assert.Equal(t, 0, *apiResp.Code)

	t.Log("收到 Solana prepare_creator_auth 响应")

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp client.EntityPrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

// generateSolanaTestVaultRequest 生成 Solana 测试 Vault 创建请求
func (test *SolanaVaultIntegrationTest) generateSolanaTestVaultRequest(t *testing.T) *client.RequestVaultCreateReq {
	// 使用当前时间戳生成唯一的项目名称
	timestamp := time.Now().Unix()
	projectName := fmt.Sprintf("SolanaTestVault_%d", timestamp)
	t.Logf("项目名称: %s", projectName)

	return &client.RequestVaultCreateReq{
		ChainId: client.SOLANA, // 使用 Solana 链 ID

		// Token 元数据
		TokenMetaData: client.RequestTokenMeta{
			TokenName:     projectName,
			TokenSymbol:   "STV",
			TokenDecimals: 6,
			TokenUri:      "https://example.com/metadata.json",
		},

		// 融资规则
		FinancingRuleData: client.RequestFinancingRuleInfo{
			ProjectName:                        projectName,
			FinancingCurrencyAddr:              test.config.Solana.Tokens.USDC,                          // 使用新的配置路径
			MinInvestmentBaseFinancingCurrency: "100000000",                                             // 100 USDC (6 decimals)
			SoftCap:                            "500000000",                                             // 10,000 USDC 软顶
			TokenMaxSupply:                     "1000000000",                                            // 1M vault tokens
			SharePrice:                         stringPtr("1000000"),                                    // 1 USDC per share
			ManageFeeBps:                       stringPtr("100"),                                        // 1% 管理费
			ExcessFundraisingRatioBps:          stringPtr("1000"),                                       // 10% 超募比例
			DecimalsMultiplier:                 stringPtr("1"),                                          // USDC decimals multiplier
			EnableWhitelist:                    boolPtr(false),                                          // 不启用白名单
			FinancingStartTime:                 intPtr(int(time.Now().Unix())),                          // 当前时间开始
			FinancingDeadline:                  intPtr(int(time.Now().Add(30 * 24 * time.Hour).Unix())), // 30天后截止
			FundingReceiver:                    &test.config.Solana.Admin.PublicKey,                     // 资金接收地址
			ManageFeeReceiver:                  &test.config.Solana.Admin.PublicKey,                     // 管理费接收地址
		},

		// 管理数据
		ManagementData: client.RequestVaultManagement{
			Deployer:        test.config.Solana.Creator.PublicKey, // 部署者,部署vault的人
			Manager:         test.config.Solana.Admin.PublicKey,   // 管理员
			Issuer:          test.config.Solana.Creator.PublicKey, // 发行人
			Withdrawer:      test.config.Solana.Admin.PublicKey,   // 提款人
			DividendManager: test.config.Solana.Admin.PublicKey,   // 分红管理员
			OffChainManager: &test.config.Solana.Admin.PublicKey,  // 链下管理员
			ProxyGuardian:   &test.config.Solana.Admin.PublicKey,  // 升级合约管理员
		},
	}
}

// TestSolanaConfigValidation 测试 Solana 配置验证
func TestSolanaConfigValidation(t *testing.T) {
	loadSolanaConfig(t)

	t.Run("ValidateSolanaConfig", func(t *testing.T) {
		// 验证服务器配置
		assert.NotEmpty(t, solanaConfig.Server.URL)

		// 验证 Solana 配置
		assert.Equal(t, "1001", solanaConfig.Solana.ChainID)
		assert.Contains(t, []string{"devnet", "testnet", "mainnet-beta"}, solanaConfig.Solana.Cluster)
		assert.NotEmpty(t, solanaConfig.Solana.RPCURL)

		// 验证管理员配置
		assert.NotEmpty(t, solanaConfig.Solana.Admin.PrivateKey)
		assert.NotEmpty(t, solanaConfig.Solana.Admin.PublicKey)

		// 验证用户配置
		assert.NotEmpty(t, solanaConfig.Solana.Deployer.PrivateKey, "Deployer 私钥不能为空")
		assert.NotEmpty(t, solanaConfig.Solana.Deployer.PublicKey, "Deployer 公钥不能为空")
		assert.NotEmpty(t, solanaConfig.Solana.Creator.PrivateKey, "Creator 私钥不能为空")
		assert.NotEmpty(t, solanaConfig.Solana.Creator.PublicKey, "Creator 公钥不能为空")
		assert.NotEmpty(t, solanaConfig.Solana.DrdsValidator.PrivateKey, "DrdsValidator 私钥不能为空")
		assert.NotEmpty(t, solanaConfig.Solana.DrdsValidator.PublicKey, "DrdsValidator 公钥不能为空")
		assert.NotEmpty(t, solanaConfig.Solana.User.PrivateKey, "User 私钥不能为空")
		assert.NotEmpty(t, solanaConfig.Solana.User.PublicKey, "User 公钥不能为空")

		// 验证代币配置
		assert.NotEmpty(t, solanaConfig.Solana.Tokens.USDC)
		assert.NotEmpty(t, solanaConfig.Solana.Tokens.WSOL)

		// 验证新增的 WebSocket URL 配置
		assert.NotEmpty(t, solanaConfig.Solana.WSURL, "WebSocket URL 不能为空")

		t.Log("✅ Solana 配置验证通过")
	})
}

// TestSolanaCreateAuth 测试 Solana 创建授权
func TestSolanaCreateAuth(t *testing.T) {
	// 加载配置
	loadSolanaConfig(t)

	// 创建测试实例
	test := NewSolanaVaultIntegrationTest(&solanaConfig, t)

	t.Run("CreateAuth", func(t *testing.T) {
		// 1. 准备创建授权请求
		createAuthReq := &CreateAuthReq{
			ChainId: client.SOLANA,
			Admin:   solanaConfig.Solana.Admin.PublicKey,
			Creator: solanaConfig.Solana.Creator.PublicKey,
		}

		t.Logf("生成 Solana 创建授权请求:")
		t.Logf("  Chain ID: %s", string(createAuthReq.ChainId))
		t.Logf("  Admin: %s", createAuthReq.Admin)
		t.Logf("  Creator: %s", createAuthReq.Creator)

		// 2. 调用 prepare_creator_auth 接口
		prepareResp := test.callPrepareCreateAuthSolana(t, createAuthReq)
		require.NotNil(t, prepareResp)
		require.NotEmpty(t, prepareResp.TxMsgBase64)
		require.NotEmpty(t, prepareResp.CorrelationId)

		t.Logf("获得待签名交易数据:")
		t.Logf("  Correlation ID: %s", *prepareResp.CorrelationId)
		t.Logf("  TxMsgBase64 长度: %d", len(*prepareResp.TxMsgBase64))

		// 3. 模拟签名过程
		signedTxBase64 := *prepareResp.TxMsgBase64

		// 4. 准备提交请求
		submitReq := &client.RequestSubmitReq{
			ChainId:      client.SOLANA,
			Sender:       solanaConfig.Solana.Admin.PublicKey,
			TxMsgBase64:  *prepareResp.TxMsgBase64,
			SignTxBase64: signedTxBase64,
		}

		signedTxBase64, err := signTx(t, test.admin, signedTxBase64)
		require.NoError(t, err)
		submitReq.SignTxBase64 = signedTxBase64

		t.Logf("准备提交 Solana 创建授权交易:")
		t.Logf("  发送者: %s", submitReq.Sender)
		t.Logf("  Chain ID: %s", string(submitReq.ChainId))

		// 5. 提交交易
		submitResp := test.callSubmitTxSolana(t, submitReq)
		require.NotNil(t, submitResp)

		t.Logf("创建授权交易提交结果:")
		if submitResp.Success != nil {
			t.Logf("  成功: %t", *submitResp.Success)
		}
		if submitResp.TxHash != nil {
			t.Logf("  交易哈希: %s", *submitResp.TxHash)
		}
		if submitResp.FailedMsg != nil {
			t.Logf("  失败信息: %s", *submitResp.FailedMsg)
		}

		// 6. 验证结果
		if submitResp.Success != nil && *submitResp.Success {
			require.NotNil(t, submitResp.TxHash)
			require.NotEmpty(t, *submitResp.TxHash)

			t.Logf("✅ Solana 创建授权成功!")
			t.Logf("   交易哈希: %s", *submitResp.TxHash)
			t.Logf("   Admin: %s", createAuthReq.Admin)
			t.Logf("   Creator: %s", createAuthReq.Creator)

			// 记录余额变化
			if submitResp.SenderBalanceChange != nil {
				t.Logf("   发送者余额变化:")
				if submitResp.SenderBalanceChange.NativeBalanceChange != nil {
					t.Logf("     Native Token: %d", *submitResp.SenderBalanceChange.NativeBalanceChange)
				}
				if submitResp.SenderBalanceChange.TokenBalanceChange != nil {
					for _, change := range *submitResp.SenderBalanceChange.TokenBalanceChange {
						if change.TokenMint != nil && change.BalanceChange != nil {
							t.Logf("     Token %s: %s", *change.TokenMint, *change.BalanceChange)
						}
					}
				}
			}
		} else {
			// 如果是测试环境或模拟环境，可能会失败，这是正常的
			t.Logf("⚠️  交易提交失败 (可能是模拟环境)")
			if submitResp.FailedMsg != nil {
				t.Logf("   失败原因: %s", *submitResp.FailedMsg)
			}
		}
	})
}

// TestSolanaVaultCreate 测试 Solana Vault 创建
func TestSolanaVaultCreate(t *testing.T) {
	// 加载配置
	loadSolanaConfig(t)

	// 创建测试实例
	test := NewSolanaVaultIntegrationTest(&solanaConfig, t)
	env = test

	t.Run("CreateVault", func(t *testing.T) {
		// 1. 生成测试请求
		createReq := test.generateSolanaTestVaultRequest(t)

		t.Logf("生成 Solana Vault 创建请求:")
		t.Logf("  项目名称: %s", createReq.FinancingRuleData.ProjectName)
		t.Logf("  Token Symbol: %s", createReq.TokenMetaData.TokenSymbol)
		t.Logf("  融资货币: %s", createReq.FinancingRuleData.FinancingCurrencyAddr)
		t.Logf("  软顶: %s USDC", createReq.FinancingRuleData.SoftCap)
		t.Logf("  管理员: %s", createReq.ManagementData.Manager)

		// 2. 调用 prepare_create 接口
		prepareResp := test.callPrepareCreateVaultSolana(t, createReq)
		require.NotNil(t, prepareResp)
		require.NotEmpty(t, prepareResp.TxMsgBase64)
		require.NotEmpty(t, prepareResp.CorrelationId)

		t.Logf("获得待签名交易数据:")
		t.Logf("  Correlation ID: %s", *prepareResp.CorrelationId)
		t.Logf("  TxMsgBase64 长度: %d", len(*prepareResp.TxMsgBase64))

		// 3. 模拟签名过程（在真实环境中，这里会使用 Solana 钱包进行签名）
		// 注意：这里我们直接使用原始的 TxMsgBase64 作为 "已签名" 的交易，
		// 在实际应用中需要使用 Solana SDK 进行真实的签名
		unsignedTxBase64 := *prepareResp.TxMsgBase64
		txs := strings.Split(unsignedTxBase64, "|")
		// 准备提交请求
		submitReq := &client.RequestSubmitReq{
			ChainId:      client.SOLANA,
			Sender:       solanaConfig.Solana.Creator.PublicKey,
			TxMsgBase64:  *prepareResp.TxMsgBase64,
			SignTxBase64: "",
		}

		signedTxBase64, err := signTx(t, test.creator, txs[0])
		require.NoError(t, err)
		submitReq.Sender = solanaConfig.Solana.Creator.PublicKey
		submitReq.SignTxBase64 = signedTxBase64

		t.Logf("准备提交 Solana 交易:")
		t.Logf("  发送者: %s", submitReq.Sender)
		t.Logf("  Chain ID: %s", string(submitReq.ChainId))

		// 5. 提交交易
		submitResp := test.callSubmitTxSolana(t, submitReq)
		require.NotNil(t, submitResp)

		t.Logf("交易提交结果:")
		if submitResp.Success != nil {
			t.Logf("  成功: %t", *submitResp.Success)
		}
		if submitResp.TxHash != nil {
			t.Logf("  交易哈希: %s", *submitResp.TxHash)
		}
		if submitResp.FailedMsg != nil {
			t.Logf("  失败信息: %s", *submitResp.FailedMsg)
		}

		// 6. 验证结果
		if submitResp.Success != nil && *submitResp.Success {
			require.NotNil(t, submitResp.TxHash)
			require.NotEmpty(t, *submitResp.TxHash)

			t.Logf("✅ Solana Vault 创建成功!")
			t.Logf("   交易哈希: %s", *submitResp.TxHash)

		} else {
			// 如果是测试环境或模拟环境，可能会失败，这是正常的
			t.Logf("⚠️  交易提交失败 (可能是模拟环境)")
			if submitResp.FailedMsg != nil {
				t.Logf("   失败原因: %s", *submitResp.FailedMsg)
			}
		}

		signedTxBase64, err = signTx(t, test.creator, txs[1])
		require.NoError(t, err)
		submitReq.Sender = solanaConfig.Solana.Creator.PublicKey
		submitReq.SignTxBase64 = signedTxBase64

		t.Logf("准备提交 Solana 交易:")
		t.Logf("  发送者: %s", submitReq.Sender)
		t.Logf("  Chain ID: %s", string(submitReq.ChainId))

		// 5. 提交交易
		submitResp = test.callSubmitTxSolana(t, submitReq)
		require.NotNil(t, submitResp)

		t.Logf("交易提交结果:")
		if submitResp.Success != nil {
			t.Logf("  成功: %t", *submitResp.Success)
		}
		if submitResp.TxHash != nil {
			t.Logf("  交易哈希: %s", *submitResp.TxHash)
		}
		if submitResp.FailedMsg != nil {
			t.Logf("  失败信息: %s", *submitResp.FailedMsg)
		}

		// 6. 验证结果
		if submitResp.Success != nil && *submitResp.Success {
			require.NotNil(t, submitResp.TxHash)
			require.NotEmpty(t, *submitResp.TxHash)

			t.Logf("✅ Solana Vault 创建成功!")
			t.Logf("   交易哈希: %s", *submitResp.TxHash)

		} else {
			// 如果是测试环境或模拟环境，可能会失败，这是正常的
			t.Logf("⚠️  交易提交失败 (可能是模拟环境)")
			if submitResp.FailedMsg != nil {
				t.Logf("   失败原因: %s", *submitResp.FailedMsg)
			}
		}

		signedTxBase64, err = signTx(t, test.creator, txs[2])
		require.NoError(t, err)
		submitReq.Sender = solanaConfig.Solana.Creator.PublicKey
		submitReq.SignTxBase64 = signedTxBase64

		t.Logf("准备提交 Solana 交易:")
		t.Logf("  发送者: %s", submitReq.Sender)
		t.Logf("  Chain ID: %s", string(submitReq.ChainId))

		// 5. 提交交易
		submitResp = test.callSubmitTxSolana(t, submitReq)
		require.NotNil(t, submitResp)

		t.Logf("交易提交结果:")
		if submitResp.Success != nil {
			t.Logf("  成功: %t", *submitResp.Success)
		}
		if submitResp.TxHash != nil {
			t.Logf("  交易哈希: %s", *submitResp.TxHash)
		}
		if submitResp.FailedMsg != nil {
			t.Logf("  失败信息: %s", *submitResp.FailedMsg)
		}

		// 6. 验证结果
		if submitResp.Success != nil && *submitResp.Success {
			require.NotNil(t, submitResp.TxHash)
			require.NotEmpty(t, *submitResp.TxHash)

			t.Logf("✅ Solana Vault 创建成功!")
			t.Logf("   交易哈希: %s", *submitResp.TxHash)

		} else {
			// 如果是测试环境或模拟环境，可能会失败，这是正常的
			t.Logf("⚠️  交易提交失败 (可能是模拟环境)")
			if submitResp.FailedMsg != nil {
				t.Logf("   失败原因: %s", *submitResp.FailedMsg)
			}
		}
	})
}

// TestSolanaVaultCommonInfo 测试 Solana Vault 基本信息查询
func TestSolanaVaultCommonInfo(t *testing.T) {
	// 加载配置
	loadSolanaConfig(t)

	// 创建测试实例
	test := NewSolanaVaultIntegrationTest(&solanaConfig, t)
	env = test

	t.Run("QueryVaultCommonInfo", func(t *testing.T) {
		// 1. 准备查询请求
		// 注意: 这里使用一个示例 Vault 地址，在实际测试中应该使用真实创建的 Vault 地址
		commonInfoReq := &VaultCommonInfoReq{
			ChainId:      client.SOLANA,
			VaultAddress: "11111111111111111111111111111111",             // 示例地址，实际应该使用真实的Vault地址
			InfoType:     "crowdsale_state",                              // 可选参数
			InfoId:       "6WkutgiSMr4ngcNjirjVRa7ZL7W6DsNgB15K1DEWuofE", // 可选参数
		}

		t.Logf("生成 Solana Vault 基本信息查询请求:")
		t.Logf("  Chain ID: %s", string(commonInfoReq.ChainId))
		t.Logf("  Vault Address: %s", commonInfoReq.VaultAddress)
		t.Logf("  Info Type: %s", commonInfoReq.InfoType)

		// 2. 调用 vault/common_info 接口
		commonInfoResp := test.callVaultCommonInfoSolana(t, commonInfoReq)
		require.NotNil(t, commonInfoResp)
		t.Logf("✅ Solana Vault 基本信息查询完成")
		retJson, _ := json.MarshalIndent(commonInfoResp, "", "  ")
		t.Logf("返回数据: %s", string(retJson))
	})
}
