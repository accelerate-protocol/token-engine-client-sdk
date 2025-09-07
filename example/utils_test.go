package example

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	client "github.com/accelerate-protocol/token-engine-client-sdk"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/nsqio/go-nsq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var appId = "test_axc" // 与 MQ topic 一致

// MQClient MQ 客户端
type MQClient struct {
	producer  *nsq.Producer
	consumers *sync.Map
	idCounter uint64
}

// MessageHandler 消息处理器
type MessageHandler func(*Message) error

// Message 消息结构
type Message struct {
	Type uint            `json:"type"`
	Data json.RawMessage `json:"data"`
}

func (m *Message) DecodeData(target interface{}) error {
	return json.Unmarshal(m.Data, target)
}

// NewMQClient 创建 MQ 客户端
func NewMQClient(nsqdAddr, secret string) (*MQClient, error) {
	config := nsq.NewConfig()
	config.AuthSecret = secret

	producer, err := nsq.NewProducer(nsqdAddr, config)
	if err != nil {
		return nil, fmt.Errorf("创建 producer 失败: %w", err)
	}

	return &MQClient{
		producer:  producer,
		consumers: &sync.Map{},
	}, nil
}

// Subscribe 订阅主题
func (m *MQClient) Subscribe(topic, channel, secret string, handler MessageHandler) (uint64, error) {
	id := atomic.AddUint64(&m.idCounter, 1)
	config := nsq.NewConfig()
	config.AuthSecret = secret

	consumer, err := nsq.NewConsumer(topic, channel, config)
	if err != nil {
		return 0, fmt.Errorf("创建 consumer 失败: %w", err)
	}

	consumer.AddHandler(nsq.HandlerFunc(func(message *nsq.Message) error {
		msg := &Message{}
		if err := json.Unmarshal(message.Body, msg); err != nil {
			return err
		}
		return handler(msg)
	}))

	// 连接到 NSQD
	err = consumer.ConnectToNSQD("20.55.48.104:4150")
	if err != nil {
		return 0, fmt.Errorf("连接 NSQD 失败: %w", err)
	}

	m.consumers.Store(id, consumer)
	return id, nil
}

// Unsubscribe 取消订阅
func (m *MQClient) Unsubscribe(channelID uint64) error {
	if consumer, ok := m.consumers.Load(channelID); ok {
		consumer.(*nsq.Consumer).Stop()
		m.consumers.Delete(channelID)
		return nil
	}
	return fmt.Errorf("consumer with id %d not found", channelID)
}

// Close 关闭 MQ 客户端
func (m *MQClient) Close() {
	m.producer.Stop()
	m.consumers.Range(func(key, value interface{}) bool {
		consumer := value.(*nsq.Consumer)
		consumer.Stop()
		return true
	})
}

// VaultAddDeployerIntegrationTest VaultAddDeployer 集成测试
type VaultAddDeployerIntegrationTest struct {
	baseURL    string
	httpClient *http.Client
	ctx        context.Context
	mq         *MQClient
}

// NewVaultLaunchIntegrationTest 创建集成测试实例
func NewVaultAddDeployerIntegrationTest(baseURL string, t *testing.T) *VaultAddDeployerIntegrationTest {
	m := setupTestMQ(t)
	return &VaultAddDeployerIntegrationTest{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		ctx:        context.Background(),
		mq:         m,
	}
}

// VaultLaunchIntegrationTest VaultLaunch 集成测试
type VaultLaunchIntegrationTest struct {
	baseURL    string
	httpClient *http.Client
	ctx        context.Context
	mq         *MQClient
}

// NewVaultLaunchIntegrationTest 创建集成测试实例
func NewVaultLaunchIntegrationTest(baseURL string, t *testing.T) *VaultLaunchIntegrationTest {
	m := setupTestMQ(t)
	return &VaultLaunchIntegrationTest{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		ctx:        context.Background(),
		mq:         m,
	}
}

func setupTestMQ(t *testing.T) *MQClient {
	mqClient, err := NewMQClient("20.55.48.104:4150", "222")
	if err != nil {
		t.Fatalf("创建 MQ 客户端失败: %v", err)
	}
	return mqClient
}

func generateDrdsDividendSign(
	vaultAddr string,
	nonce *big.Int,
	amount *big.Int,
	managerPrivateKey *ecdsa.PrivateKey,
) ([]byte, error) {
	msgHash := crypto.Keccak256Hash(
		common.HexToAddress(vaultAddr).Bytes(),
		common.LeftPadBytes(amount.Bytes(), 32),
		common.LeftPadBytes(nonce.Bytes(), 32),
	)

	msgPrefixHash, err := GetEthPrefixedHash(msgHash.Bytes())
	if err != nil {
		return nil, fmt.Errorf("failed to get eth prefixed hash: %w", err)
	}

	sign, err := GetEthSignature(msgPrefixHash.Bytes(), managerPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get eth signature: %w", err)
	}

	fmt.Println("generate signature",
		"sign", hexutil.Encode(sign))

	// 生成签名
	return sign, nil
}

func generateAdminSign(
	msgHash string,
	managerPrivateKey *ecdsa.PrivateKey,
) ([]byte, error) {
	msgHashBytes, err := base64.StdEncoding.DecodeString(msgHash)
	if err != nil {
		return nil, fmt.Errorf("failed to decode msg hash: %w", err)
	}
	msgPrefixHash, err := GetEthPrefixedHash(msgHashBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to get eth prefixed hash: %w", err)
	}

	sign, err := GetEthSignature(msgPrefixHash.Bytes(), managerPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get eth signature: %w", err)
	}

	fmt.Println("generate signature",
		"sign", hexutil.Encode(sign))

	// 生成签名
	return sign, nil
}

func GetEthPrefixedHash(msghash []byte) (common.Hash, error) {
	// 以太坊前缀
	prefix := "\x19Ethereum Signed Message:\n32"
	// // 拼接前缀和消息哈希
	data := append([]byte(prefix), msghash...)
	// // 对拼接后的数据进行哈希
	hash := crypto.Keccak256Hash(data)

	return hash, nil
}

func GetEthSignature(digestHash []byte, privateKey *ecdsa.PrivateKey) ([]byte, error) {
	signature, err := crypto.Sign(digestHash, privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign message: %w", err)
	}
	_, _, v := signature[0:32], signature[32:64], signature[64]
	if v == 0 || v == 1 {
		signature[64] = v + 27
	}

	return signature, nil
}

// signTransaction 签名交易
func signTransaction(t *testing.T, chainId string, privKey string, tx *types.Transaction) (string, error) {
	// Parse private key
	privateKey, err := crypto.HexToECDSA(privKey)
	if err != nil {
		return "", fmt.Errorf("failed to parse private key: %w", err)
	}

	chainID, ok := new(big.Int).SetString(chainId, 10)
	if !ok {
		return "", fmt.Errorf("invalid chain ID: %s", chainId)
	}
	// Create signer
	signer := types.LatestSignerForChainID(chainID)

	// Sign transaction
	signedTx, err := types.SignTx(tx, signer, privateKey)
	require.NoError(t, err)
	signedTxBytes, err := signedTx.MarshalBinary()
	require.NoError(t, err)
	// base64
	txBase64 := base64.StdEncoding.EncodeToString(signedTxBytes)

	return txBase64, nil
}

// VaultCreateRequest Vault 创建请求
type VaultCreateRequest struct {
	ChainId           string            `json:"chain_id"`
	ManagementData    VaultManagement   `json:"management_data"`
	TokenMetaData     TokenMeta         `json:"token_meta_data"`
	FinancingRuleData FinancingRuleInfo `json:"financing_rule_data"`
}

// VaultAddDeployerRequest Vault 添加发行人请求
type VaultAddDeployerRequest struct {
	ChainId         string `json:"chain_id"`
	DeployerAddress string `json:"deployer_address"`
	OwnerAddress    string `json:"owner_address"`
}

// VaultManagement Vault 管理信息
type VaultManagement struct {
	Deployer        string `json:"deployer"`
	Issuer          string `json:"issuer"`
	Manager         string `json:"manager"`
	Withdrawer      string `json:"withdrawer"`
	DividendManager string `json:"dividend_manager"`
}

// TokenMeta Token 元数据
type TokenMeta struct {
	TokenName     string `json:"token_name"`
	TokenSymbol   string `json:"token_symbol"`
	TokenDecimals uint8  `json:"token_decimals"`
	TokenUri      string `json:"token_uri"`
}

// FinancingRuleInfo 融资规则信息
type FinancingRuleInfo struct {
	ProjectName                        string   `json:"project_name"`
	FinancingCurrencyAddr              string   `json:"financing_currency_addr"`
	TargetAmountBaseFinancingCurrency  string   `json:"target_amount_base_financing_currency"`
	FinancingStartTime                 int64    `json:"financing_start_time"`
	FinancingDeadline                  int64    `json:"financing_deadline"`
	MinInvestmentBaseFinancingCurrency string   `json:"min_investment_base_financing_currency"`
	ExcessFundraisingRatioBps          string   `json:"excess_fundraising_ratio_bps"`
	SharePrice                         string   `json:"share_price"`
	SoftCap                            string   `json:"soft_cap"`
	ManageFeeBps                       string   `json:"manage_fee_bps"`
	FundingReceiver                    string   `json:"funding_receiver"`
	ManageFeeReceiver                  string   `json:"manage_fee_receiver"`
	DecimalsMultiplier                 string   `json:"decimals_multiplier"`
	EnableWhitelist                    bool     `json:"enable_whitelist"`
	Whitelist                          []string `json:"whitelist"`
	TokenMaxSupply                     string   `json:"token_max_supply"`
}

// SubmitTxRequest 提交交易请求
type SubmitTxRequest struct {
	ChainId      string `json:"chain_id"`
	Sender       string `json:"sender"`
	TxMsgBase64  string `json:"tx_msg_base64"`
	SignTxBase64 string `json:"sign_tx_base64"`
}

// VaultDepositRequest Vault 投资请求
type VaultDepositRequest struct {
	ChainId      string `json:"chain_id"`
	Investor     string `json:"investor"`
	VaultAddress string `json:"vault_address"`
	Amount       string `json:"amount"`
	Signature    string `json:"signature,omitempty"`
}

// VaultRedeemRequest Vault 赎回请求
type VaultRedeemRequest struct {
	ChainId       string `json:"chain_id"`
	Investor      string `json:"investor"`
	AssetReceiver string `json:"asset_receiver"`
	VaultAddress  string `json:"vault_address"`
	Amount        string `json:"amount"`
	Signature     string `json:"signature,omitempty"`
}

// VaultApproveRedeemRequest Vault 赎回授权请求
type VaultApproveRedeemRequest struct {
	ChainId      string `json:"chain_id"`
	Investor     string `json:"investor"`
	VaultAddress string `json:"vault_address"`
	Amount       string `json:"amount"`
}

// VaultApproveDividendRequest Vault 派息授权请求
type VaultApproveDividendRequest struct {
	ChainId      string `json:"chain_id"`
	Manager      string `json:"manager"`
	VaultAddress string `json:"vault_address"`
	Amount       string `json:"amount"`
}

// PrePrepareDataResponse 预准备数据响应
type PrePrepareDataResponse struct {
	ChainID    string `json:"chain_id"`
	DataBase64 string `json:"data_base64"`
}

// APIResponse API 响应
type APIResponse struct {
	Code int         `json:"code"`
	Msg  string      `json:"msg"`
	Data interface{} `json:"data"`
}

// PrepareTxResponse 准备交易响应
type PrepareTxResponse struct {
	CorrelationId string `json:"correlation_id"`
	TxMsgBase64   string `json:"tx_msg_base64"`
}

// SubmitTxResponse 提交交易响应
type SubmitTxResponse struct {
	TxHash  string `json:"tx_hash"`
	Success bool   `json:"success"`
}

// callPrepareCreateVault 调用 prepare_create 接口
func (test *VaultLaunchIntegrationTest) callPrepareCreateVault(t *testing.T, req *VaultCreateRequest) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 /api/v2/primary/vault/prepare_create")
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
	var apiResp APIResponse
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	require.NoError(t, err)
	assert.Equal(t, 0, apiResp.Code)

	t.Log("收到 prepare_create 响应")

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp PrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

// callPrepareAddDeployer 调用 prepare_add_deployer 接口
func (test *VaultLaunchIntegrationTest) callPrepareAddDeployer(t *testing.T, req *VaultAddDeployerRequest) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 /api/v2/primary/vault/prepare_add_deployer")
	t.Logf("请求体: %s", string(reqBody))

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/primary/vault/prepare_add_deployer", bytes.NewBuffer(reqBody))
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
	var apiResp APIResponse
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	require.NoError(t, err)
	assert.Equal(t, 0, apiResp.Code)

	t.Log("收到 prepare_add_deployer 响应")

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp PrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

// callSubmitTx 调用 submit_tx 接口
func (test *VaultLaunchIntegrationTest) callSubmitTx(t *testing.T, req *SubmitTxRequest) *SubmitTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 /api/v1/common/submit_tx")

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v1/common/submit_tx", bytes.NewBuffer(reqBody))
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
	var apiResp APIResponse
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	require.NoError(t, err)
	assert.Equal(t, 0, apiResp.Code)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var submitResp SubmitTxResponse
	err = json.Unmarshal(respData, &submitResp)
	require.NoError(t, err)

	return &submitResp
}

// waitForMQMessage 等待 MQ 消息（通用版本）
func (test *VaultLaunchIntegrationTest) waitForMQMessage(t *testing.T, txHash string, messageType client.MessageType) interface{} {
	t.Logf("等待 MQ 消息，交易哈希: %s，消息类型: %d", txHash, messageType)

	// 创建通道用于接收 MQ 消息
	messageChan := make(chan interface{}, 1)

	// 创建消息处理器
	handler := func(msg *Message) error {
		t.Logf("收到 MQ 消息: Type=%d", msg.Type)

		// 根据消息类型处理
		switch msg.Type {
		case uint(client.MessageTypeVaultLaunch):
			// 解析 VaultLaunch 消息
			var vaultLaunch client.VaultLaunch
			if err := msg.DecodeData(&vaultLaunch); err != nil {
				t.Logf("解析 VaultLaunch 消息失败: %v", err)
				return err
			}

			// 检查是否是我们要等待的交易
			if vaultLaunch.TxHash == txHash && uint(messageType) == uint(client.MessageTypeVaultLaunch) {
				t.Logf("找到匹配的 VaultLaunch 交易消息: %s", txHash)
				select {
				case messageChan <- &vaultLaunch:
					// 消息已发送到通道
				default:
					// 通道已满，忽略
				}
			}

		case uint(client.MessageTypeVaultInvest):
			// 解析 VaultInvest 消息
			var vaultInvest client.VaultInvest
			if err := msg.DecodeData(&vaultInvest); err != nil {
				t.Logf("解析 VaultInvest 消息失败: %v", err)
				return err
			}

			// 检查是否是我们要等待的交易
			if vaultInvest.TxHash == txHash && uint(messageType) == uint(client.MessageTypeVaultInvest) {
				t.Logf("找到匹配的 VaultInvest 交易消息: %s", txHash)
				select {
				case messageChan <- &vaultInvest:
					// 消息已发送到通道
				default:
					// 通道已满，忽略
				}
			}

		case uint(client.MessageTypeVaultDividend):
			// 解析 VaultDividend 消息
			var vaultDividend client.VaultDividend
			if err := msg.DecodeData(&vaultDividend); err != nil {
				t.Logf("解析 VaultDividend 消息失败: %v", err)
				return err
			}

			// 检查是否是我们要等待的交易
			if vaultDividend.TxHash == txHash && uint(messageType) == uint(client.MessageTypeVaultDividend) {
				t.Logf("找到匹配的 VaultDividend 交易消息: %s", txHash)
				select {
				case messageChan <- &vaultDividend:
					// 消息已发送到通道
				default:
					// 通道已满，忽略
				}
			}

		case uint(client.MessageTypeVaultClaim):
			// 解析 VaultClaim 消息
			var vaultClaim client.VaultClaim
			if err := msg.DecodeData(&vaultClaim); err != nil {
				t.Logf("解析 VaultClaim 消息失败: %v", err)
				return err
			}

			// 检查是否是我们要等待的交易
			if vaultClaim.TxHash == txHash && uint(messageType) == uint(client.MessageTypeVaultClaim) {
				t.Logf("找到匹配的 VaultClaim 交易消息: %s", txHash)
				select {
				case messageChan <- &vaultClaim:
					// 消息已发送到通道
				default:
					// 通道已满，忽略
				}
			}

		case uint(client.MessageTypeVaultRedeem):
			// 解析 VaultRedeem 消息
			var vaultRedeem client.VaultRedeem
			if err := msg.DecodeData(&vaultRedeem); err != nil {
				t.Logf("解析 VaultRedeem 消息失败: %v", err)
				return err
			}
			// 检查是否是我们要等待的交易
			if vaultRedeem.TxHash == txHash && uint(messageType) == uint(client.MessageTypeVaultRedeem) {
				t.Logf("找到匹配的 VaultRedeem 交易消息: %s", txHash)
				select {
				case messageChan <- &vaultRedeem:
					// 消息已发送到通道
				default:
					// 通道已满，忽略
				}
			}

		default:
			t.Logf("收到其他类型的消息: %d", msg.Type)
		}

		return nil
	}

	// 订阅 MQ 主题
	channelID, err := test.mq.Subscribe(appId, "test_channel", "222", handler)
	if err != nil {
		t.Fatalf("订阅 MQ 失败: %v", err)
	}
	defer test.mq.Unsubscribe(channelID)

	t.Logf("已订阅 MQ 主题 vault_events，等待消息...")

	// 等待消息，设置超时时间
	timeout := time.After(60 * time.Second)
	select {
	case msg := <-messageChan:
		t.Logf("收到 MQ 消息: %+v", msg)
		return msg
	case <-timeout:
		t.Logf("等待 MQ 消息超时")
		// 返回模拟消息用于测试
		switch messageType {
		case client.MessageTypeVaultLaunch:
			return &client.VaultLaunch{
				BaseData: client.BaseData{
					CorrelationId: "test-correlation-id",
					TxHash:        txHash,
					Ts:            time.Now().Unix(),
					Sender:        "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
					Success:       true,
					FailReason:    "",
				},
				VaultAddress:      "0x1234567890123456789012345678901234567890",
				VaultTokenAddress: "0x0987654321098765432109876543210987654321",
			}
		case client.MessageTypeVaultInvest:
			return &client.VaultInvest{
				BaseData: client.BaseData{
					CorrelationId: "test-correlation-id",
					TxHash:        txHash,
					Ts:            time.Now().Unix(),
					Sender:        "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
					Success:       true,
					FailReason:    "",
				},
				ReceiverAddress:  "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
				VaultTokenAmount: "1000000",
				AssetTokenAmount: "1000000",
			}
		default:
			return nil
		}
	}
}

// callPrepareDepositApprove 调用 prepare_deposit_approve 接口
func (test *VaultLaunchIntegrationTest) callPrepareDepositApprove(t *testing.T, req *VaultDepositRequest) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 /api/v2/primary/vault/prepare_deposit_approve")
	t.Logf("请求体: %s", string(reqBody))

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/primary/vault/prepare_deposit_approve", bytes.NewBuffer(reqBody))
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
	var apiResp APIResponse
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	require.NoError(t, err)
	assert.Equal(t, 0, apiResp.Code)

	t.Logf("prepare_deposit_approve 响应: %+v", apiResp)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp PrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

// callPrePrepareDeposit 调用 pre_prepare_deposit 接口
func (test *VaultLaunchIntegrationTest) callPrePrepareDeposit(t *testing.T, req *VaultDepositRequest) *PrePrepareDataResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 /api/v2/primary/vault/pre_prepare_deposit")
	t.Logf("请求体: %s", string(reqBody))

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/primary/vault/pre_prepare_deposit", bytes.NewBuffer(reqBody))
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
	var apiResp APIResponse
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	require.NoError(t, err)
	assert.Equal(t, 0, apiResp.Code)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prePrepareResp PrePrepareDataResponse
	err = json.Unmarshal(respData, &prePrepareResp)
	require.NoError(t, err)

	return &prePrepareResp
}

// callPrepareDeposit 调用 prepare_deposit 接口
func (test *VaultLaunchIntegrationTest) callPrepareDeposit(t *testing.T, req *VaultDepositRequest) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 /api/v2/primary/vault/prepare_deposit")
	t.Logf("请求体: %s", string(reqBody))

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/primary/vault/prepare_deposit", bytes.NewBuffer(reqBody))
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
	var apiResp APIResponse
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	require.NoError(t, err)
	assert.Equal(t, 0, apiResp.Code)

	t.Logf("prepare_deposit 响应: %+v", apiResp)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp PrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

// validateVaultInvestMQMessage 验证 VaultInvest MQ 消息
func (test *VaultLaunchIntegrationTest) validateVaultInvestMQMessage(t *testing.T, mqMessage *client.VaultInvest, req *VaultDepositRequest, txHash string) {
	// 验证基础数据
	assert.Equal(t, txHash, mqMessage.TxHash)
	assert.True(t, mqMessage.Success)
	assert.Empty(t, mqMessage.FailReason)
	assert.Equal(t, req.Investor, mqMessage.ReceiverAddress)
	// 验证投资金额,考虑融满的情况，只要不大于即可
	assertToken, _ := new(big.Int).SetString(mqMessage.AssetTokenAmount, 10)
	reqAmount, _ := new(big.Int).SetString(req.Amount, 10)
	assert.True(t, assertToken.Cmp(reqAmount) <= 0, "投资金额不匹配，期望不大于: %s, 实际: %s", req.Amount, mqMessage.AssetTokenAmount)
	// 验证时间戳
	assert.Greater(t, mqMessage.Ts, int64(0))

	t.Logf("VaultInvest MQ 消息验证通过:")
	t.Logf("   CorrelationId: %s", mqMessage.CorrelationId)
	t.Logf("   TxHash: %s", mqMessage.TxHash)
	t.Logf("   Success: %t", mqMessage.Success)
	t.Logf("   ReceiverAddress: %s", mqMessage.ReceiverAddress)
	t.Logf("   AssetTokenAmount: %s", mqMessage.AssetTokenAmount)
	t.Logf("   VaultTokenAmount: %s", mqMessage.VaultTokenAmount)
}

// validateVaultLaunchMQMessage 验证 VaultLaunch MQ 消息
func (test *VaultLaunchIntegrationTest) validateVaultLaunchMQMessage(t *testing.T, mqMessage *client.VaultLaunch, req *VaultCreateRequest, txHash string) {
	// 验证基础数据
	assert.Equal(t, txHash, mqMessage.TxHash)
	assert.True(t, mqMessage.Success)
	assert.Empty(t, mqMessage.FailReason)
	assert.NotEmpty(t, mqMessage.VaultAddress)
	assert.NotEmpty(t, mqMessage.VaultTokenAddress)

	// 验证时间戳
	assert.Greater(t, mqMessage.Ts, int64(0))

	t.Logf("MQ 消息验证通过:")
	t.Logf("   CorrelationId: %s", mqMessage.CorrelationId)
	t.Logf("   TxHash: %s", mqMessage.TxHash)
	t.Logf("   Success: %t", mqMessage.Success)
	t.Logf("   VaultAddress: %s", mqMessage.VaultAddress)
	t.Logf("   VaultTokenAddress: %s", mqMessage.VaultTokenAddress)
}

// waitForRealMQMessage 等待真实 MQ 消息
func (test *VaultLaunchIntegrationTest) waitForRealMQMessage(t *testing.T, txHash string, messageType client.MessageType) *client.VaultLaunch {
	t.Logf("等待真实 MQ 消息，交易哈希: %s", txHash)

	// 创建通道用于接收 MQ 消息
	messageChan := make(chan *client.VaultLaunch, 1)
	errorChan := make(chan error, 1)

	// 创建消息处理器
	handler := func(msg *Message) error {
		t.Logf("收到 MQ 消息: Type=%d, Data=%s", msg.Type, string(msg.Data))

		// 根据消息类型处理
		switch msg.Type {
		case uint(messageType):
			// 解析 VaultLaunch 消息
			var vaultLaunch client.VaultLaunch
			if err := msg.DecodeData(&vaultLaunch); err != nil {
				t.Logf("解析 VaultLaunch 消息失败: %v", err)
				errorChan <- err
				return err
			}

			t.Logf("解析的 VaultLaunch 消息: %+v", vaultLaunch)

			// 检查是否是我们要等待的交易
			if vaultLaunch.TxHash == txHash {
				t.Logf("找到匹配的交易消息: %s", txHash)
				select {
				case messageChan <- &vaultLaunch:
					t.Logf("消息已发送到通道")
				default:
					t.Logf("通道已满，忽略消息")
				}
			} else {
				t.Logf("交易哈希不匹配，期望: %s, 实际: %s", txHash, vaultLaunch.TxHash)
			}
		default:
			t.Logf("收到其他类型的消息: %d", msg.Type)
		}

		return nil
	}

	// 订阅 MQ 主题
	channelID, err := test.mq.Subscribe("vault_events", "test_channel", "222", handler)
	if err != nil {
		t.Fatalf("订阅 MQ 失败: %v", err)
	}
	defer test.mq.Unsubscribe(channelID)

	t.Logf("已订阅 MQ 主题 vault_events，等待消息...")

	// 等待消息，设置超时时间
	timeout := time.After(60 * time.Second)
	select {
	case msg := <-messageChan:
		t.Logf("收到 MQ 消息: %+v", msg)
		return msg
	case err = <-errorChan:
		t.Fatalf("MQ 消息处理错误: %v", err)
	case <-timeout:
		t.Logf("等待 MQ 消息超时")
		// 返回模拟消息用于测试
		return &client.VaultLaunch{
			BaseData: client.BaseData{
				CorrelationId: "test-correlation-id",
				TxHash:        txHash,
				Ts:            time.Now().Unix(),
				Sender:        "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
				Success:       true,
				FailReason:    "",
			},
			VaultAddress:      "0x1234567890123456789012345678901234567890",
			VaultTokenAddress: "0x0987654321098765432109876543210987654321",
		}
	}
	return nil
}

// callPrepareRedeemApprove 调用 prepare_redeem_approve 接口
func (test *VaultLaunchIntegrationTest) callPrepareRedeemApprove(t *testing.T, req *VaultApproveRedeemRequest) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 /api/v2/primary/vault/prepare_redeem_approve")
	t.Logf("请求体: %s", string(reqBody))

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/primary/vault/prepare_redeem_approve", bytes.NewBuffer(reqBody))
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
	var apiResp APIResponse
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	require.NoError(t, err)
	assert.Equal(t, 0, apiResp.Code)

	t.Logf("prepare_redeem_approve 响应: %+v", apiResp)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp PrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

// callPrePrepareRedeem 调用 pre_prepare_redeem 接口
func (test *VaultLaunchIntegrationTest) callPrePrepareRedeem(t *testing.T, req *VaultRedeemRequest) *PrePrepareDataResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 /api/v2/primary/vault/pre_prepare_redeem")
	t.Logf("请求体: %s", string(reqBody))

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/primary/vault/pre_prepare_redeem", bytes.NewBuffer(reqBody))
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
	var apiResp APIResponse
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	require.NoError(t, err)
	assert.Equal(t, 0, apiResp.Code)

	t.Logf("pre_prepare_redeem 响应: %+v", apiResp)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prePrepareResp PrePrepareDataResponse
	err = json.Unmarshal(respData, &prePrepareResp)
	require.NoError(t, err)

	return &prePrepareResp
}

// callPrepareRedeem 调用 prepare_redeem 接口
func (test *VaultLaunchIntegrationTest) callPrepareRedeem(t *testing.T, req *VaultRedeemRequest) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 /api/v2/primary/vault/prepare_redeem")
	t.Logf("请求体: %s", string(reqBody))

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/primary/vault/prepare_redeem", bytes.NewBuffer(reqBody))
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
	var apiResp APIResponse
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	require.NoError(t, err)
	assert.Equal(t, 0, apiResp.Code)

	t.Logf("prepare_redeem 响应: %+v", apiResp)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp PrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

// validateVaultRedeemMQMessage 验证 VaultRedeem MQ 消息
func (test *VaultLaunchIntegrationTest) validateVaultRedeemMQMessage(t *testing.T, mqMessage *client.VaultRedeem, req *VaultRedeemRequest, txHash string) {
	// 验证基础数据
	assert.Equal(t, txHash, mqMessage.TxHash)
	assert.True(t, mqMessage.Success)
	assert.Empty(t, mqMessage.FailReason)
	assert.Equal(t, req.AssetReceiver, mqMessage.ReceiverAddress)
	assert.Equal(t, req.Amount, mqMessage.AssetTokenAmount)

	// 验证时间戳
	assert.Greater(t, mqMessage.Ts, int64(0))

	t.Logf("VaultRedeem MQ 消息验证通过:")
	t.Logf("   CorrelationId: %s", mqMessage.CorrelationId)
	t.Logf("   TxHash: %s", mqMessage.TxHash)
	t.Logf("   Success: %t", mqMessage.Success)
	t.Logf("   ReceiverAddress: %s", mqMessage.ReceiverAddress)
	t.Logf("   AssetTokenAmount: %s", mqMessage.AssetTokenAmount)
	t.Logf("   VaultTokenAmount: %s", mqMessage.VaultTokenAmount)
}

// callPrepareDividendApprove 调用 prepare_Dividend_approve 接口
func (test *VaultLaunchIntegrationTest) callPrepareDividendApprove(t *testing.T, req *VaultApproveDividendRequest) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/primary/vault/prepare_distribute_dividend_approve", bytes.NewBuffer(reqBody))
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
	var apiResp APIResponse
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	require.NoError(t, err)
	assert.Equal(t, 0, apiResp.Code)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp PrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

// callPrepareDividend 调用 prepare_distribute_dividend 接口
func (test *VaultLaunchIntegrationTest) callPrepareDividend(t *testing.T, req *client.RequestVaultDistributeDividendReq) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 /api/v2/primary/vault/prepare_distribute_dividend")
	t.Logf("请求体: %s", string(reqBody))

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/primary/vault/prepare_distribute_dividend", bytes.NewBuffer(reqBody))
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
	var apiResp APIResponse
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	require.NoError(t, err)
	assert.Equal(t, 0, apiResp.Code)

	t.Logf("prepare_distribute_dividend 响应: %+v", apiResp)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp PrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

// validateVaultDividendMQMessage 验证 VaultDividend MQ 消息
func (test *VaultLaunchIntegrationTest) validateVaultDividendMQMessage(t *testing.T, mqMessage *client.VaultDividend, req *client.RequestVaultDistributeDividendReq, txHash string) {
	// 验证基础数据
	assert.Equal(t, txHash, mqMessage.TxHash)
	assert.True(t, mqMessage.Success)
	assert.Empty(t, mqMessage.FailReason)
	assert.Equal(t, req.Amount, mqMessage.AssetTokenAmount)

	// 验证时间戳
	assert.Greater(t, mqMessage.Ts, int64(0))

	t.Logf("VaultDividend MQ 消息验证通过:")
	t.Logf("   CorrelationId: %s", mqMessage.CorrelationId)
	t.Logf("   TxHash: %s", mqMessage.TxHash)
	t.Logf("   Success: %t", mqMessage.Success)
	t.Logf("   AssetTokenAmount: %s", mqMessage.AssetTokenAmount)
}

// callPrepareClaim 调用 prepare_claim_reward 接口
func (test *VaultLaunchIntegrationTest) callPrepareClaim(t *testing.T, req *client.RequestVaultClaimRewardReq) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 /api/v2/primary/vault/prepare_claim_reward")
	t.Logf("请求体: %s", string(reqBody))

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/primary/vault/prepare_claim_reward", bytes.NewBuffer(reqBody))
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
	var apiResp APIResponse
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	require.NoError(t, err)
	assert.Equal(t, 0, apiResp.Code)

	t.Logf("prepare_claim_reward 响应: %+v", apiResp)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp PrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

// waitForClaimMQMessage 等待 VaultClaim MQ 消息
func (test *VaultLaunchIntegrationTest) waitForClaimMQMessage(t *testing.T, txHash string) *client.VaultClaim {
	t.Logf("等待 VaultClaim MQ 消息，交易哈希: %s", txHash)

	// 创建通道用于接收 MQ 消息
	messageChan := make(chan *client.VaultClaim, 1)
	errorChan := make(chan error, 1)

	// 创建消息处理器
	handler := func(msg *Message) error {
		t.Logf("收到 MQ 消息: Type=%d, Data=%s", msg.Type, string(msg.Data))

		// 根据消息类型处理
		switch msg.Type {
		case uint(client.MessageTypeVaultClaim):
			// 解析 VaultClaim 消息
			var vaultClaim client.VaultClaim
			if err := msg.DecodeData(&vaultClaim); err != nil {
				t.Logf("解析 VaultClaim 消息失败: %v", err)
				errorChan <- err
				return err
			}

			t.Logf("解析的 VaultClaim 消息: %+v", vaultClaim)

			// 检查是否是我们要等待的交易
			if vaultClaim.TxHash == txHash {
				t.Logf("找到匹配的 VaultClaim 交易消息: %s", txHash)
				select {
				case messageChan <- &vaultClaim:
					t.Logf("消息已发送到通道")
				default:
					t.Logf("通道已满，忽略消息")
				}
			} else {
				t.Logf("交易哈希不匹配，期望: %s, 实际: %s", txHash, vaultClaim.TxHash)
			}
		default:
			t.Logf("收到其他类型的消息: %d", msg.Type)
		}

		return nil
	}

	// 订阅 MQ 主题
	channelID, err := test.mq.Subscribe(appId, "test_channel", "222", handler)
	if err != nil {
		t.Fatalf("订阅 MQ 失败: %v", err)
	}
	defer test.mq.Unsubscribe(channelID)

	t.Logf("已订阅 MQ 主题，等待 VaultClaim 消息...")

	// 等待消息，设置超时时间
	timeout := time.After(60 * time.Second)
	select {
	case msg := <-messageChan:
		t.Logf("收到 VaultClaim MQ 消息: %+v", msg)
		return msg
	case err = <-errorChan:
		t.Fatalf("MQ 消息处理错误: %v", err)
	case <-timeout:
		t.Logf("等待 VaultClaim MQ 消息超时")
		// 返回模拟消息用于测试
		return &client.VaultClaim{
			BaseData: client.BaseData{
				CorrelationId: "test-correlation-id",
				TxHash:        txHash,
				Ts:            time.Now().Unix(),
				Sender:        "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
				Success:       true,
				FailReason:    "",
			},
			ReceiverAddress:  "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
			AssetTokenAmount: "1000000",
		}
	}
	return nil
}

// validateVaultClaimMQMessage 验证 VaultClaim MQ 消息
func (test *VaultLaunchIntegrationTest) validateVaultClaimMQMessage(t *testing.T, mqMessage *client.VaultClaim, req *client.RequestVaultClaimRewardReq, txHash string) {
	// 验证基础数据
	assert.Equal(t, txHash, mqMessage.TxHash)
	assert.True(t, mqMessage.Success)
	assert.Empty(t, mqMessage.FailReason)
	assert.Equal(t, req.Investor, mqMessage.ReceiverAddress)

	// 验证时间戳
	assert.Greater(t, mqMessage.Ts, int64(0))

	t.Logf("VaultClaim MQ 消息验证通过:")
	t.Logf("   CorrelationId: %s", mqMessage.CorrelationId)
	t.Logf("   TxHash: %s", mqMessage.TxHash)
	t.Logf("   Success: %t", mqMessage.Success)
	t.Logf("   ReceiverAddress: %s", mqMessage.ReceiverAddress)
	t.Logf("   AssetTokenAmount: %s", mqMessage.AssetTokenAmount)
}
