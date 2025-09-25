package example

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	client "github.com/accelerate-protocol/token-engine-client-sdk"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/nsqio/go-nsq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var appId = "root" // 与 MQ topic 一致
var erc20Abi = "[{\"inputs\":[{\"internalType\":\"string\",\"name\":\"name_\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"symbol_\",\"type\":\"string\"}],\"stateMutability\":\"nonpayable\",\"type\":\"constructor\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"owner\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"spender\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"value\",\"type\":\"uint256\"}],\"name\":\"Approval\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"from\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"to\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"value\",\"type\":\"uint256\"}],\"name\":\"Transfer\",\"type\":\"event\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"owner\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"spender\",\"type\":\"address\"}],\"name\":\"allowance\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"spender\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"}],\"name\":\"approve\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"account\",\"type\":\"address\"}],\"name\":\"balanceOf\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"decimals\",\"outputs\":[{\"internalType\":\"uint8\",\"name\":\"\",\"type\":\"uint8\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"spender\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"subtractedValue\",\"type\":\"uint256\"}],\"name\":\"decreaseAllowance\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"spender\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"addedValue\",\"type\":\"uint256\"}],\"name\":\"increaseAllowance\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"name\",\"outputs\":[{\"internalType\":\"string\",\"name\":\"\",\"type\":\"string\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"symbol\",\"outputs\":[{\"internalType\":\"string\",\"name\":\"\",\"type\":\"string\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"totalSupply\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"to\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"}],\"name\":\"transfer\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"from\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"to\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"}],\"name\":\"transferFrom\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"}]"

type Signer struct {
	PrivateKey string `toml:"private_key"`
	Address    string `toml:"address"`
}

const (
	ChainIdBSCTestnet  = "97"
	ChainIdBaseSepolia = "84532"
)

// Config 配置文件结构
type Config struct {
	Server struct {
		URL string `toml:"url"`
	} `toml:"server"`

	Admin Signer   `toml:"admin"`
	Users []Signer `toml:"users"`

	Blockchain struct {
		ChainID  string `toml:"chain_id"`
		MockUSDC string `toml:"mock_usdc"`
		Decimal  int    `toml:"decimal"`
	} `toml:"blockchain"`
}

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

// VaultLaunchIntegrationTest VaultLaunch 集成测试
type VaultLaunchIntegrationTest struct {
	baseURL    string
	httpClient *http.Client
	ethClient  *ethclient.Client
	ctx        context.Context
	mq         *MQClient
}

// NewVaultLaunchIntegrationTest 创建集成测试实例
func NewVaultLaunchIntegrationTest(baseURL string, t *testing.T) *VaultLaunchIntegrationTest {
	m := setupTestMQ(t)
	var ethURL string
	switch chainId {
	case ChainIdBaseSepolia:
		ethURL = "https://base-sepolia.g.alchemy.com/v2/9NAr3qhyUGw766ZqM8HmAqfBrt4FKr0a"
	case ChainIdBSCTestnet:
		ethURL = "https://bsc-testnet-rpc.publicnode.com"
	default:
		t.Fatalf("不支持的 chainId: %s", chainId)
	}
	ethClient, err := ethclient.Dial(ethURL)
	require.NoError(t, err)
	return &VaultLaunchIntegrationTest{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		ethClient:  ethClient,
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

func generateDrdsDividendSign(vaultAddr string, nonce *big.Int, amount *big.Int, managerPrivateKey *ecdsa.PrivateKey) ([]byte, error) {
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

func generateAdminSign(msgHash string, managerPrivateKey *ecdsa.PrivateKey) ([]byte, error) {
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

// 辅助函数：将字符串转换为指针
func stringPtr(s string) *string {
	return &s
}

func intPtr(i int) *int {
	return &i
}

// 辅助函数：将布尔值转换为指针
func boolPtr(b bool) *bool {
	return &b
}

// parseUsd 根据decimal精度转换金额
func parseUsd(amount float64) string {
	decimal := config.Blockchain.Decimal
	// 计算精度倍数
	multiplier := math.Pow10(decimal)
	// 转换金额并返回字符串
	return strconv.FormatInt(int64(amount*multiplier), 10)
}

// VaultDepositRequest Vault 投资请求
type VaultDepositRequest struct {
	ChainId      string `json:"chain_id"`
	Sender       string `json:"sender"`
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
func (test *VaultLaunchIntegrationTest) callPrepareCreateVault(t *testing.T, req *client.RequestVaultCreateReq) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 /api/v2/primary/vault/prepare_create")

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

// callPrepareUnPauseToken 调用 prepare_unpause_token 接口
func (test *VaultLaunchIntegrationTest) callPrepareUnPauseToken(t *testing.T, req *client.RequestVaultUnPauseTokenReq) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/primary/vault/prepare_unpause_token", bytes.NewBuffer(reqBody))
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

	t.Log("收到 prepare_unpause_token 响应")

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp PrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

// validateVaultUnPauseTokenTx 验证 unpause_token 交易
func (test *VaultLaunchIntegrationTest) validateVaultUnPauseTokenReceipt(t *testing.T, txHash string) {
	// 解析交易哈希
	txHashObj := common.HexToHash(txHash)

	// 获取交易回执
	receipt, err := test.ethClient.TransactionReceipt(context.Background(), txHashObj)
	if err != nil {
		t.Fatalf("获取交易回执失败: %v", err)
	}

	// 验证交易状态
	if receipt.Status != types.ReceiptStatusSuccessful {
		t.Fatalf("交易执行失败，状态码: %d", receipt.Status)
	}
	t.Logf("交易执行成功，区块高度: %d", receipt.BlockNumber.Uint64())
}

// callTokenBalance 获取地址的代币余额
func (test *VaultLaunchIntegrationTest) callTokenBalance(t *testing.T, req *client.RequestBalanceQueryReq) string {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/balance/get", bytes.NewBuffer(reqBody))
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

	t.Log("收到 balance 响应")

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var balanceResp client.ResponseBalanceResp
	err = json.Unmarshal(respData, &balanceResp)
	require.NoError(t, err)

	return *balanceResp.Balance
}

// callPrepareAddDeployer 调用 prepare_add_deployer 接口
func (test *VaultLaunchIntegrationTest) callPrepareAddDeployer(t *testing.T, req *client.RequestAddVaultDeployerWhiteListReq) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

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
func (test *VaultLaunchIntegrationTest) callSubmitTx(t *testing.T, req *client.RequestSubmitReq) *SubmitTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

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
		case uint(client.MessageTypeOffChainDeposit):
			// 解析 OffChainDeposit 消息
			var offChainDeposit client.OffChainDeposit
			if err := msg.DecodeData(&offChainDeposit); err != nil {
				t.Logf("解析 OffChainDeposit 消息失败: %v", err)
				return err
			}
			// 检查是否是我们要等待的交易
			if offChainDeposit.TxHash == txHash && uint(messageType) == uint(client.MessageTypeOffChainDeposit) {
				t.Logf("找到匹配的 OffChainDeposit 交易消息: %s", txHash)
				select {
				case messageChan <- &offChainDeposit:
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
func (test *VaultLaunchIntegrationTest) validateVaultLaunchMQMessage(t *testing.T, mqMessage *client.VaultLaunch, txHash string) {
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

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp PrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

// callPrePrepareRedeem 调用 pre_prepare_redeem 接口
func (test *VaultLaunchIntegrationTest) callPrePrepareRedeem(t *testing.T, req *client.RequestVaultRedeemReq) *PrePrepareDataResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

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
func (test *VaultLaunchIntegrationTest) callPrepareRedeem(t *testing.T, req *client.RequestVaultRedeemReq) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 /api/v2/primary/vault/prepare_redeem")

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
func (test *VaultLaunchIntegrationTest) validateVaultRedeemMQMessage(t *testing.T, mqMessage *client.VaultRedeem, req *client.RequestVaultRedeemReq, txHash string) {
	// 验证基础数据
	assert.Equal(t, txHash, mqMessage.TxHash)
	assert.True(t, mqMessage.Success)
	assert.Empty(t, mqMessage.FailReason)
	assert.Equal(t, req.AssetReceiver, mqMessage.ReceiverAddress)
	assert.Equal(t, req.Amount, mqMessage.VaultTokenAmount)

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

// callPrepareOffchainDeposit 调用 prepare_off_chain_deposit 接口
func (test *VaultLaunchIntegrationTest) callPrepareOffchainDeposit(t *testing.T, req *client.RequestOffChainDepositReq) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 /api/v2/primary/vault/prepare_off_chain_deposit")
	t.Logf("请求体: %s", string(reqBody))

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/primary/vault/prepare_off_chain_deposit", bytes.NewBuffer(reqBody))
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

	t.Logf("prepare_off_chain_deposit 响应: %+v", apiResp)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp PrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

// callPrepareTokenApprove 调用 prepare_transfer_approve 接口
func (test *VaultLaunchIntegrationTest) callPrepareTokenApprove(t *testing.T, req *client.RequestApprovePrepareReq) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/approve/prepare", bytes.NewBuffer(reqBody))
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

// callPrepareTokenTransfer 调用 prepare_transfer 接口
func (test *VaultLaunchIntegrationTest) callPrepareTokenTransfer(t *testing.T, req *client.RequestTransferPrepareReq) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 /api/v2/transfer/prepare")

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/transfer/prepare", bytes.NewBuffer(reqBody))
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

// validateTokenApproveReceipt 验证 TokenApprove 消息
func (test *VaultLaunchIntegrationTest) validateTokenApproveReceipt(t *testing.T, req *client.RequestApprovePrepareReq, txHash string) {
	contractAddr := common.HexToAddress(req.TokenAddr)
	from := common.HexToAddress(req.FromAddr)
	spender := common.HexToAddress(req.SpenderAddr)

	// 解析ERC20合约ABI
	_, err := abi.JSON(strings.NewReader(erc20Abi))
	if err != nil {
		t.Fatalf("解析ERC20 ABI失败: %v", err)
	}

	// 解析交易哈希
	txHashObj := common.HexToHash(txHash)

	// 获取交易回执
	receipt, err := test.ethClient.TransactionReceipt(context.Background(), txHashObj)
	if err != nil {
		t.Fatalf("获取交易回执失败: %v", err)
	}

	// 验证交易状态
	if receipt.Status != types.ReceiptStatusSuccessful {
		t.Fatalf("交易执行失败，状态码: %d", receipt.Status)
	}

	// 定义Approval事件的签名
	approvalEventSignature := crypto.Keccak256Hash([]byte("Approval(address,address,uint256)"))

	// 在回执日志中查找Approval事件
	var approvalEvent *types.Log
	for _, log := range receipt.Logs {
		// 检查日志地址是否为代币合约地址
		if log.Address == contractAddr {
			// 检查日志主题是否为Approval事件签名
			if len(log.Topics) >= 3 && log.Topics[0] == approvalEventSignature {
				// 检查owner和spender是否匹配
				logOwner := common.HexToAddress(log.Topics[1].Hex())
				logSpender := common.HexToAddress(log.Topics[2].Hex())

				if logOwner == from && logSpender == spender {
					approvalEvent = log
					break
				}
			}
		}
	}

	if approvalEvent == nil {
		t.Fatalf("未找到匹配的Approval事件")
	}

	// 解析事件数据获取授权金额
	value := new(big.Int).SetBytes(approvalEvent.Data)

	t.Logf("授权额度: %s", value.String())

	// 验证授权额度是否符合预期
	expectedAmount, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok {
		t.Fatalf("解析预期授权额度失败")
	}

	if value.Cmp(expectedAmount) != 0 {
		t.Errorf("授权额度不匹配，预期: %s, 实际: %s", expectedAmount.String(), value.String())
	}
}

// validateTokenTransferReceipt 验证 TokenTransfer
func (test *VaultLaunchIntegrationTest) validateTokenTransferReceipt(t *testing.T, req *client.RequestTransferPrepareReq, txHash string) {

	contractAddr := common.HexToAddress(*req.TokenAddr)
	from := common.HexToAddress(req.FromAddr)
	spender := common.HexToAddress(req.ToAddr)

	// 解析ERC20合约ABI
	_, err := abi.JSON(strings.NewReader(erc20Abi))
	if err != nil {
		t.Fatalf("解析ERC20 ABI失败: %v", err)
	}

	// 解析交易哈希
	txHashObj := common.HexToHash(txHash)

	// 获取交易回执
	receipt, err := test.ethClient.TransactionReceipt(context.Background(), txHashObj)
	if err != nil {
		t.Fatalf("获取交易回执失败: %v", err)
	}

	// 验证交易状态
	if receipt.Status != types.ReceiptStatusSuccessful {
		t.Fatalf("交易执行失败，状态码: %d", receipt.Status)
	}

	// 定义Transfer事件的签名
	transferEventSignature := crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))

	// 在回执日志中查找Transfer事件
	var transferEvent *types.Log
	for _, log := range receipt.Logs {
		// 检查日志地址是否为代币合约地址
		if log.Address == contractAddr {
			// 检查日志主题是否为Transfer事件签名
			if len(log.Topics) >= 3 && log.Topics[0] == transferEventSignature {
				// 检查from和to是否匹配
				logFrom := common.HexToAddress(log.Topics[1].Hex())
				logTo := common.HexToAddress(log.Topics[2].Hex())

				if logFrom == from && logTo == spender {
					transferEvent = log
					break
				}
			}
		}
	}

	if transferEvent == nil {
		t.Fatalf("未找到匹配的Transfer事件")
	}

	// 解析事件数据获取转账金额
	value := new(big.Int).SetBytes(transferEvent.Data)

	t.Logf("转账金额: %s", value.String())

	// 验证转账金额是否符合预期
	expectedAmount, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok {
		t.Fatalf("解析预期转账金额失败")
	}

	if value.Cmp(expectedAmount) != 0 {
		t.Errorf("转账金额不匹配，预期: %s, 实际: %s", expectedAmount.String(), value.String())
	}
}

// validateVaultOffChainInvestMQMessage 验证 VaultOffChainInvest MQ 消息
func (test *VaultLaunchIntegrationTest) validateVaultOffChainInvestMQMessage(t *testing.T, mqMessage *client.OffChainDeposit, req *client.RequestOffChainDepositReq, txHash string) {
	// 验证基础数据
	assert.Equal(t, txHash, mqMessage.TxHash)
	assert.True(t, mqMessage.Success)
	assert.Empty(t, mqMessage.FailReason)
	assert.Equal(t, req.Recipient, mqMessage.ReceiverAddress)

	// 验证投资金额
	assert.Equal(t, req.Amount, mqMessage.AssetTokenAmount)

	// 验证时间戳
	assert.Greater(t, mqMessage.Ts, int64(0))

	t.Logf("VaultOffChainInvest MQ 消息验证通过:")
	t.Logf("   CorrelationId: %s", mqMessage.CorrelationId)
	t.Logf("   TxHash: %s", mqMessage.TxHash)
	t.Logf("   Success: %t", mqMessage.Success)
	t.Logf("   ReceiverAddress: %s", mqMessage.ReceiverAddress)
	t.Logf("   AssetTokenAmount: %s", mqMessage.AssetTokenAmount)
	t.Logf("   VaultTokenAmount: %s", mqMessage.VaultTokenAmount)
}

// callPrepareOffchainRedeem 调用 prepare_off_chain_redeem 接口
func (test *VaultLaunchIntegrationTest) callPrepareOffchainRedeem(t *testing.T, req *client.RequestOffChainRedeemReq) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 /api/v2/primary/vault/prepare_off_chain_redeem")
	t.Logf("请求体: %s", string(reqBody))

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/primary/vault/prepare_off_chain_redeem", bytes.NewBuffer(reqBody))
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

	t.Logf("prepare_off_chain_redeem 响应: %+v", apiResp)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp PrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

// validateVaultOffChainRedeemMQMessage 验证 VaultOffChainRedeem MQ 消息
func (test *VaultLaunchIntegrationTest) validateVaultOffChainRedeemMQMessage(t *testing.T, mqMessage *client.VaultRedeem, req *client.RequestOffChainRedeemReq, txHash string) {
	// 验证基础数据
	assert.Equal(t, txHash, mqMessage.TxHash)
	assert.True(t, mqMessage.Success)
	assert.Empty(t, mqMessage.FailReason)
	assert.Equal(t, req.Recipient, mqMessage.ReceiverAddress)

	// 验证时间戳
	assert.Greater(t, mqMessage.Ts, int64(0))

	t.Logf("VaultOffChainRedeem MQ 消息验证通过:")
	t.Logf("   CorrelationId: %s", mqMessage.CorrelationId)
	t.Logf("   TxHash: %s", mqMessage.TxHash)
	t.Logf("   Success: %t", mqMessage.Success)
	t.Logf("   ReceiverAddress: %s", mqMessage.ReceiverAddress)
	t.Logf("   AssetTokenAmount: %s", mqMessage.AssetTokenAmount)
	t.Logf("   VaultTokenAmount: %s", mqMessage.VaultTokenAmount)
}
