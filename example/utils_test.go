package example

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/google/go-querystring/query"
	"math/big"
	"net/http"
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

var appId = "test_axc" // 与 MQ topic 一致
var erc20Abi = "[{\"inputs\":[{\"internalType\":\"string\",\"name\":\"name_\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"symbol_\",\"type\":\"string\"}],\"stateMutability\":\"nonpayable\",\"type\":\"constructor\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"owner\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"spender\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"value\",\"type\":\"uint256\"}],\"name\":\"Approval\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"from\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"to\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"value\",\"type\":\"uint256\"}],\"name\":\"Transfer\",\"type\":\"event\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"owner\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"spender\",\"type\":\"address\"}],\"name\":\"allowance\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"spender\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"}],\"name\":\"approve\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"account\",\"type\":\"address\"}],\"name\":\"balanceOf\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"decimals\",\"outputs\":[{\"internalType\":\"uint8\",\"name\":\"\",\"type\":\"uint8\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"spender\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"subtractedValue\",\"type\":\"uint256\"}],\"name\":\"decreaseAllowance\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"spender\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"addedValue\",\"type\":\"uint256\"}],\"name\":\"increaseAllowance\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"name\",\"outputs\":[{\"internalType\":\"string\",\"name\":\"\",\"type\":\"string\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"symbol\",\"outputs\":[{\"internalType\":\"string\",\"name\":\"\",\"type\":\"string\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"totalSupply\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"to\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"}],\"name\":\"transfer\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"from\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"to\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"}],\"name\":\"transferFrom\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"}]"
var nsqdAddress = "20.55.48.104:4150"

type Signer struct {
	PrivateKey string `toml:"private_key"`
	Address    string `toml:"address"`
}

const (
	ChainIdBSCTestnet  = "97"
	ChainIdBSCMainnet  = "56"
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
		Decimal  uint64 `toml:"decimal"`
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
	err = consumer.ConnectToNSQD(nsqdAddress)
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
	case ChainIdBSCMainnet:
		ethURL = "https://bsc-rpc.publicnode.com"
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

// ValidateTransactionResultsWithMQMessages 验证交易结果（使用预订阅的MQ消息）
func (vm *ValidationManager) ValidateTransactionResultsWithMQMessages(t *testing.T, results []*SubmitResult, mqMessages []interface{}) *ValidationResult {
	successCount := uint(0)
	failedCount := uint(0)

	// 验证交易状态
	for _, result := range results {
		if result.TxHash == "" {
			t.Logf("交易哈希为空")
			failedCount++
			continue
		}

		// 等待交易上链
		time.Sleep(10 * time.Second)
		receipt, err := vm.test.ethClient.TransactionReceipt(context.Background(), common.HexToHash(result.TxHash))
		if err != nil {
			t.Logf("获取交易收据失败: %v", err)
			failedCount++
			continue
		}

		if receipt.Status != types.ReceiptStatusSuccessful {
			t.Logf("交易失败: %s", result.TxHash)
			failedCount++
		} else {
			t.Logf("交易成功: %s", result.TxHash)
			successCount++
		}
	}

	// 验证MQ消息
	for i, msg := range mqMessages {
		if msg == nil {
			t.Logf("第%d个MQ消息为空", i+1)
		} else {
			t.Logf("收到第%d个MQ消息: %+v", i+1, msg)
		}
	}

	return &ValidationResult{
		SuccessCount: successCount,
		FailedCount:  failedCount,
	}
}

// TransactionManager 交易管理器
type TransactionManager struct {
	test    *VaultLaunchIntegrationTest
	chainId string
}

// NewTransactionManager 创建交易管理器
func NewTransactionManager(test *VaultLaunchIntegrationTest, chainId string) *TransactionManager {
	return &TransactionManager{
		test:    test,
		chainId: chainId,
	}
}

// MQSubscription MQ订阅信息
type MQSubscription struct {
	ChannelID   uint64 // 修改为uint64类型
	MessageChan chan interface{}
	TxHash      string
	MessageType client.MessageType
}

// PreSubscribeMQ 预订阅MQ消息，在提交交易前调用
func (tm *TransactionManager) PreSubscribeMQ(t *testing.T, expectedTxHashes []string, messageType client.MessageType) []*MQSubscription {
	var subscriptions []*MQSubscription

	for _, expectedTxHash := range expectedTxHashes {
		// 为每个预期的交易哈希创建订阅
		uniqueChannelName := fmt.Sprintf("pre_sub_%d_%s", time.Now().UnixNano(), expectedTxHash[:8])
		messageChan := make(chan interface{}, 1)

		// 创建消息处理器
		handler := func(msg *Message) error {
			//t.Logf("预订阅收到 MQ 消息: Type=%d (预期 TxHash: %s)", msg.Type, expectedTxHash)

			// 根据消息类型处理
			switch msg.Type {
			case uint(client.MessageTypeTokenTransfer):
				var tokenTransfer client.TokenTransfer
				if err := msg.DecodeData(&tokenTransfer); err != nil {
					t.Logf("解析 TokenTransfer 消息失败: %v", err)
					return err
				}

				// 检查是否是我们要等待的交易
				if tokenTransfer.TxHash == expectedTxHash && uint(messageType) == uint(client.MessageTypeTokenTransfer) {
					t.Logf("预订阅找到匹配的 TokenTransfer 交易消息: %s", expectedTxHash)
					select {
					case messageChan <- &tokenTransfer:
						// 消息已发送到通道
					default:
						// 通道已满，忽略
					}
				} else {
					//t.Logf("预订阅 TokenTransfer 消息不匹配: 收到 TxHash=%s, 期望 TxHash=%s", tokenTransfer.TxHash, expectedTxHash)
				}

			// 可以根据需要添加其他消息类型的处理
			default:
				t.Logf("预订阅收到其他类型的消息: %d", msg.Type)
			}

			return nil
		}

		// 订阅 MQ 主题
		channelID, err := tm.test.mq.Subscribe(appId, uniqueChannelName, "222", handler)
		if err != nil {
			t.Fatalf("预订阅 MQ 失败: %v", err)
		}

		subscription := &MQSubscription{
			ChannelID:   channelID, // channelID已经是uint64类型
			MessageChan: messageChan,
			TxHash:      expectedTxHash,
			MessageType: messageType,
		}
		subscriptions = append(subscriptions, subscription)

		//t.Logf("已预订阅 MQ 主题 (channel: %s) 等待交易: %s", uniqueChannelName, expectedTxHash)
	}

	return subscriptions
}

// WaitForMQMessages 等待预订阅的MQ消息
func (tm *TransactionManager) WaitForMQMessages(t *testing.T, subscriptions []*MQSubscription, timeoutSeconds int) []interface{} {
	var results []interface{}
	timeout := time.After(time.Duration(timeoutSeconds) * time.Second)

	for i, sub := range subscriptions {
		t.Logf("等待第%d个预订阅消息: %s", i+1, sub.TxHash)

		select {
		case msg := <-sub.MessageChan:
			t.Logf("收到第%d个预订阅 MQ 消息: %+v", i+1, msg)
			results = append(results, msg)
		case <-timeout:
			t.Logf("第%d个预订阅消息等待超时: %s", i+1, sub.TxHash)
			results = append(results, nil)
		}
	}

	return results
}

// CleanupSubscriptions 清理订阅
func (tm *TransactionManager) CleanupSubscriptions(subscriptions []*MQSubscription) {
	for _, sub := range subscriptions {
		tm.test.mq.Unsubscribe(sub.ChannelID) // sub.ChannelID现在是uint64类型
	}
}

// TransactionType 交易类型
type TransactionType int

const (
	TokenTransfer TransactionType = iota
	TokenApprove
)

// WrapTransaction 包装的交易
type WrapTransaction struct {
	TxHash         string
	TxBase64       string
	SignedTxBase64 string
	Type           TransactionType
	ChangeNonce    bool
}

// SubmitResult 提交结果
type SubmitResult struct {
	TxHash      string
	TxType      TransactionType
	ChangeNonce bool
}

// PrepareTransferTransactions 准备转账交易
func (tm *TransactionManager) PrepareTransferTransactions(t *testing.T, req1, req2 *client.RequestTransferPrepareReq, changeNonce, aheadTx bool, senderAddr, senderPrivateKey string) []*WrapTransaction {
	// 准备第一笔交易
	transferResp1 := tm.test.callPrepareTokenTransfer(t, req1)
	require.NotNil(t, transferResp1)
	require.NotEmpty(t, transferResp1.TxMsgBase64)

	tx1 := &types.Transaction{}
	data1, err := base64.StdEncoding.DecodeString(transferResp1.TxMsgBase64)
	require.NoError(t, err)
	err = tx1.UnmarshalBinary(data1)
	require.NoError(t, err)

	// 准备第二笔交易
	transferResp2 := tm.test.callPrepareTokenTransfer(t, req2)
	require.NotNil(t, transferResp2)
	require.NotEmpty(t, transferResp2.TxMsgBase64)

	tx2 := &types.Transaction{}
	data2, err := base64.StdEncoding.DecodeString(transferResp2.TxMsgBase64)
	require.NoError(t, err)
	err = tx2.UnmarshalBinary(data2)
	require.NoError(t, err)

	var submitTxs []*WrapTransaction

	if changeNonce {
		// 如果需要修改nonce，则重新获取最新nonce并构造交易
		currentNonce := tm.test.getNonce(t, senderAddr)
		t.Logf("当前账户nonce: %d", currentNonce)

		if aheadTx {
			// 交易插队场景：在两笔转账交易之间插入一笔approve交易
			approveReq := &client.RequestApprovePrepareReq{
				ChainId:     client.RequestChainId(tm.chainId),
				FromAddr:    senderAddr,
				TokenAddr:   mockUSDC,
				SpenderAddr: req1.ToAddr,
				Amount:      "1000000", // 1 USDT (6 decimals)
			}

			approveResp := tm.test.callPrepareTokenApprove(t, approveReq)
			require.NotNil(t, approveResp)
			require.NotEmpty(t, approveResp.TxMsgBase64)

			approveTx := &types.Transaction{}
			approveData, err := base64.StdEncoding.DecodeString(approveResp.TxMsgBase64)
			require.NoError(t, err)
			err = approveTx.UnmarshalBinary(approveData)
			require.NoError(t, err)

			// 修改交易nonce
			approveModified := modifyTransactionNonce(approveTx, currentNonce)
			tx1Modified := modifyTransactionNonce(tx1, currentNonce+1)
			tx2Modified := modifyTransactionNonce(tx2, currentNonce+2)

			signedApproveTx, err := signTransaction(t, tm.chainId, senderPrivateKey, approveModified)
			require.NoError(t, err)
			signedTx1, err := signTransaction(t, tm.chainId, senderPrivateKey, tx1Modified)
			require.NoError(t, err)
			signedTx2, err := signTransaction(t, tm.chainId, senderPrivateKey, tx2Modified)
			require.NoError(t, err)

			// 编码原始交易为base64
			approveOriginalData, err := approveTx.MarshalBinary()
			require.NoError(t, err)
			approveOriginalBase64 := base64.StdEncoding.EncodeToString(approveOriginalData)

			tx1OriginalData, err := tx1.MarshalBinary()
			require.NoError(t, err)
			tx1OriginalBase64 := base64.StdEncoding.EncodeToString(tx1OriginalData)

			tx2OriginalData, err := tx2.MarshalBinary()
			require.NoError(t, err)
			tx2OriginalBase64 := base64.StdEncoding.EncodeToString(tx2OriginalData)

			// 编码已签名交易为base64
			approveData2, err := signedApproveTx.MarshalBinary()
			require.NoError(t, err)
			approveBase64 := base64.StdEncoding.EncodeToString(approveData2)

			tx1Data, err := signedTx1.MarshalBinary()
			require.NoError(t, err)
			tx1Base64 := base64.StdEncoding.EncodeToString(tx1Data)

			tx2Data, err := signedTx2.MarshalBinary()
			require.NoError(t, err)
			tx2Base64 := base64.StdEncoding.EncodeToString(tx2Data)

			// 提交顺序：approve -> tx1 -> tx2
			submitTxs = []*WrapTransaction{
				{TxHash: signedApproveTx.Hash().String(), TxBase64: approveOriginalBase64, SignedTxBase64: approveBase64, Type: TokenApprove},
				{TxHash: signedTx1.Hash().String(), TxBase64: tx1OriginalBase64, SignedTxBase64: tx1Base64, Type: TokenTransfer, ChangeNonce: true},
				{TxHash: signedTx2.Hash().String(), TxBase64: tx2OriginalBase64, SignedTxBase64: tx2Base64, Type: TokenTransfer, ChangeNonce: true},
			}
			t.Logf("交易插队：approve交易nonce=%d，第一笔转账nonce=%d，第二笔转账nonce=%d",
				currentNonce, currentNonce+1, currentNonce+2)
		} else {
			// 正常场景：第一笔交易使用当前nonce，第二笔交易使用nonce+1
			tx1Modified := modifyTransactionNonce(tx1, currentNonce)
			tx2Modified := modifyTransactionNonce(tx2, currentNonce+1)

			// 签名修改后的交易
			signedTx1, err := signTransaction(t, tm.chainId, senderPrivateKey, tx1Modified)
			require.NoError(t, err)
			signedTx2, err := signTransaction(t, tm.chainId, senderPrivateKey, tx2Modified)
			require.NoError(t, err)

			// 编码原始交易为base64
			tx1ModifiedData, err := tx1Modified.MarshalBinary()
			require.NoError(t, err)
			tx1ModifiedBase64 := base64.StdEncoding.EncodeToString(tx1ModifiedData)

			tx2ModifiedData, err := tx2Modified.MarshalBinary()
			require.NoError(t, err)
			tx2ModifiedBase64 := base64.StdEncoding.EncodeToString(tx2ModifiedData)

			// 编码已签名交易为base64
			signedTx1Data, err := signedTx1.MarshalBinary()
			require.NoError(t, err)
			signedTx1Base64 := base64.StdEncoding.EncodeToString(signedTx1Data)

			signedTx2Data, err := signedTx2.MarshalBinary()
			require.NoError(t, err)
			signedTx2Base64 := base64.StdEncoding.EncodeToString(signedTx2Data)

			submitTxs = []*WrapTransaction{
				{TxHash: signedTx1.Hash().String(), TxBase64: tx1ModifiedBase64, SignedTxBase64: signedTx1Base64, Type: TokenTransfer, ChangeNonce: true},
				{TxHash: signedTx2.Hash().String(), TxBase64: tx2ModifiedBase64, SignedTxBase64: signedTx2Base64, Type: TokenTransfer, ChangeNonce: true},
			}
			t.Logf("正常顺序：第一笔交易nonce=%d，第二笔交易nonce=%d", currentNonce, currentNonce+1)
		}
	} else {
		// 不修改nonce，两笔交易相同，需要签名
		signedTx1, err := signTransaction(t, tm.chainId, senderPrivateKey, tx1)
		require.NoError(t, err)
		signedTx2, err := signTransaction(t, tm.chainId, senderPrivateKey, tx2)
		require.NoError(t, err)

		// 编码原始交易为base64
		tx1OriginalData, err := tx1.MarshalBinary()
		require.NoError(t, err)
		tx1OriginalBase64 := base64.StdEncoding.EncodeToString(tx1OriginalData)

		tx2OriginalData, err := tx2.MarshalBinary()
		require.NoError(t, err)
		tx2OriginalBase64 := base64.StdEncoding.EncodeToString(tx2OriginalData)

		// 编码已签名交易为base64
		tx1Data, err := signedTx1.MarshalBinary()
		require.NoError(t, err)
		tx1Base64 := base64.StdEncoding.EncodeToString(tx1Data)

		tx2Data, err := signedTx2.MarshalBinary()
		require.NoError(t, err)
		tx2Base64 := base64.StdEncoding.EncodeToString(tx2Data)

		submitTxs = []*WrapTransaction{
			{TxHash: signedTx1.Hash().String(), TxBase64: tx1OriginalBase64, SignedTxBase64: tx1Base64, Type: TokenTransfer, ChangeNonce: false},
			{TxHash: signedTx2.Hash().String(), TxBase64: tx2OriginalBase64, SignedTxBase64: tx2Base64, Type: TokenTransfer, ChangeNonce: false},
		}
		t.Logf("不修改nonce，两笔交易相同")
	}

	return submitTxs
}

// SubmitTransactions 提交交易列表
func (tm *TransactionManager) SubmitTransactions(t *testing.T, transactions []*WrapTransaction, senderAddr, senderPrivateKey string) []*SubmitResult {
	var submitResults []*SubmitResult

	for i, wTx := range transactions {
		t.Logf("提交第%d笔交易", i+1)

		// 提交交易
		submitResp := tm.test.callSubmitTx(t, &client.RequestSubmitReq{
			ChainId:      client.CommonChainID(tm.chainId),
			Sender:       senderAddr,
			TxMsgBase64:  wTx.TxBase64,
			SignTxBase64: wTx.SignedTxBase64, // 使用相同的已签名交易数据
		})

		if submitResp != nil && submitResp.TxHash != "" {
			submitResults = append(submitResults, &SubmitResult{
				TxHash:      submitResp.TxHash,
				TxType:      wTx.Type,
				ChangeNonce: wTx.ChangeNonce,
			})
			t.Logf("第%d笔交易提交成功，TxHash: %s", i+1, submitResp.TxHash)
		} else {
			t.Logf("第%d笔交易提交失败", i+1)
		}

		// 等待一段时间再提交下一笔交易
		time.Sleep(2 * time.Second)
	}

	return submitResults
}

// ValidationManager 验证管理器
type ValidationManager struct {
	test *VaultLaunchIntegrationTest
}

// NewValidationManager 创建验证管理器
func NewValidationManager(test *VaultLaunchIntegrationTest) *ValidationManager {
	return &ValidationManager{
		test: test,
	}
}

// ValidationResult 验证结果
type ValidationResult struct {
	SuccessCount uint
	FailedCount  uint
}

// ValidateTransactionResults 验证交易结果
func (vm *ValidationManager) ValidateTransactionResults(t *testing.T, submitResults []*SubmitResult, transferReq *client.RequestTransferPrepareReq) *ValidationResult {
	t.Logf("等待MQ消息推送...")
	successCount := uint(0)
	failedCount := uint(0)

	// 等待足够的时间让交易被处理
	time.Sleep(10 * time.Second)

	for i, res := range submitResults {
		if res.TxHash != "" {
			txHash := res.TxHash
			t.Logf("验证第%d笔交易状态，TxHash: %s", i+1, res.TxHash)

			// 检查交易收据来验证交易状态
			receipt, err := vm.test.ethClient.TransactionReceipt(context.Background(), common.HexToHash(txHash))
			if err != nil {
				t.Logf("第%d笔交易收据获取失败: %v", i+1, err)
				failedCount++
				continue
			}

			if receipt.Status == 1 {
				t.Logf("第%d笔交易成功，Gas Used: %d", i+1, receipt.GasUsed)
				successCount++

				// 对于转账交易，等待并验证TokenTransfer MQ消息
				if res.TxType == TokenTransfer {
					t.Logf("等待第%d笔转账交易的TokenTransfer MQ消息", i+1)

					// 等待TokenTransfer MQ消息
					mqMessageInterface := vm.test.waitForMQMessage(t, txHash, client.MessageTypeTokenTransfer)
					tokenTransferMsg, ok := mqMessageInterface.(*client.TokenTransfer)
					assert.True(t, ok, "转换MQ消息类型失败")
					if tokenTransferMsg != nil {
						t.Logf("收到第%d笔交易的TokenTransfer MQ消息", i+1)

						// 验证MQ消息内容
						vm.test.validateTokenTransferMQMessage(t, tokenTransferMsg, transferReq, txHash, res.ChangeNonce)

						// 对比回执事件和MQ消息的一致性
						vm.test.compareReceiptWithMQMessage(t, transferReq, txHash, tokenTransferMsg)
					} else {
						t.Logf("第%d笔交易未收到TokenTransfer MQ消息", i+1)
					}
				}
			} else {
				t.Logf("第%d笔交易失败，Status: %d", i+1, receipt.Status)
				failedCount++
			}

			// 可以进一步检查事件日志
			if len(receipt.Logs) > 0 {
				t.Logf("第%d笔交易产生了%d个事件", i+1, len(receipt.Logs))
			}
		} else {
			failedCount++
		}
	}

	return &ValidationResult{
		SuccessCount: successCount,
		FailedCount:  failedCount,
	}
}

// ValidateTestCase 验证测试用例结果
func (vm *ValidationManager) ValidateTestCase(t *testing.T, testCaseName string, result *ValidationResult, expectSuccessNum uint, changeNonce, aheadTx bool) {
	t.Logf("测试用例 '%s' 完成，成功交易数: %d，失败交易数: %d，期望成功数: %d",
		testCaseName, result.SuccessCount, result.FailedCount, expectSuccessNum)

	// 验证结果是否符合期望
	if result.SuccessCount == expectSuccessNum {
		t.Logf("✅ 测试用例 '%s' 验证通过", testCaseName)
	} else {
		t.Errorf("❌ 测试用例 '%s' 验证失败，期望成功数: %d，实际成功数: %d",
			testCaseName, expectSuccessNum, result.SuccessCount)
	}
}

func setupTestMQ(t *testing.T) *MQClient {
	mqClient, err := NewMQClient(nsqdAddress, "222")
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

func generateDrdsFinishEpochSign(vaultAddr string, epochId *big.Int, assetAmount *big.Int, managerPrivateKey *ecdsa.PrivateKey) ([]byte, error) {
	msgHash := crypto.Keccak256Hash(
		common.HexToAddress(vaultAddr).Bytes(),
		common.LeftPadBytes(epochId.Bytes(), 32),
		common.LeftPadBytes(assetAmount.Bytes(), 32),
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

func signTransaction(t *testing.T, chainId string, privKey string, tx *types.Transaction) (*types.Transaction, error) {
	// Parse private key
	privateKey, err := crypto.HexToECDSA(privKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	chainID, ok := new(big.Int).SetString(chainId, 10)
	if !ok {
		return nil, fmt.Errorf("invalid chain ID: %s", chainId)
	}
	// Create signer
	signer := types.LatestSignerForChainID(chainID)

	// Sign transaction
	signedTx, err := types.SignTx(tx, signer, privateKey)
	require.NoError(t, err)

	return signedTx, nil
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
func parseUsd(amount int64) string {
	decimal := config.Blockchain.Decimal
	// 计算精度倍数
	multiplier := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimal)), nil)
	scaleUpAmount := new(big.Int).Mul(big.NewInt(int64(amount)), multiplier)
	// 转换金额并返回字符串
	return scaleUpAmount.String()
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

	// 为每次调用生成唯一的 channel 名称，避免消息混乱
	uniqueChannelName := fmt.Sprintf("test_channel_%d_%s", time.Now().UnixNano(), txHash[:8])

	// 创建消息处理器
	handler := func(msg *Message) error {
		t.Logf("收到 MQ 消息: Type=%d (等待 TxHash: %s)", msg.Type, txHash)

		// 根据消息类型处理
		switch msg.Type {
		case uint(client.MessageTypeVaultLaunch), uint(client.MessageTypeFundVaultLaunch):
			// 解析 VaultLaunch 消息
			var vaultLaunch client.VaultLaunch
			if err := msg.DecodeData(&vaultLaunch); err != nil {
				t.Logf("解析 VaultLaunch 消息失败: %v", err)
				return err
			}

			// 检查是否是我们要等待的交易
			if vaultLaunch.TxHash == txHash && (uint(messageType) == uint(client.MessageTypeVaultLaunch) || uint(messageType) == uint(client.MessageTypeFundVaultLaunch)) {
				t.Logf("找到匹配的 VaultLaunch 交易消息: %s", txHash)
				select {
				case messageChan <- &vaultLaunch:
					// 消息已发送到通道
				default:
					// 通道已满，忽略
				}
			} else {
				t.Logf("VaultLaunch 消息不匹配: 收到 TxHash=%s, 期望 TxHash=%s", vaultLaunch.TxHash, txHash)
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
			} else {
				t.Logf("VaultInvest 消息不匹配: 收到 TxHash=%s, 期望 TxHash=%s", vaultInvest.TxHash, txHash)
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
			} else {
				t.Logf("VaultDividend 消息不匹配: 收到 TxHash=%s, 期望 TxHash=%s", vaultDividend.TxHash, txHash)
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
			} else {
				t.Logf("OffChainDeposit 消息不匹配: 收到 TxHash=%s, 期望 TxHash=%s", offChainDeposit.TxHash, txHash)
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
			} else {
				t.Logf("VaultClaim 消息不匹配: 收到 TxHash=%s, 期望 TxHash=%s", vaultClaim.TxHash, txHash)
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
			} else {
				t.Logf("VaultRedeem 消息不匹配: 收到 TxHash=%s, 期望 TxHash=%s", vaultRedeem.TxHash, txHash)
			}

		case uint(client.MessageTypeWithdrawManageFee):
			// 解析 WithdrawManageFee 消息
			var withdrawFee client.VaultWithdrawFee
			if err := msg.DecodeData(&withdrawFee); err != nil {
				t.Logf("解析 WithdrawManageFee 消息失败: %v", err)
				return err
			}
			// 检查是否是我们要等待的交易
			if withdrawFee.TxHash == txHash && uint(messageType) == uint(client.MessageTypeWithdrawManageFee) {
				t.Logf("找到匹配的 WithdrawManageFee 交易消息: %s", txHash)
				select {
				case messageChan <- &withdrawFee:
					// 消息已发送到通道
				default:
					// 通道已满，忽略
				}
			} else {
				t.Logf("WithdrawManageFee 消息不匹配: 收到 TxHash=%s, 期望 TxHash=%s", withdrawFee.TxHash, txHash)
			}

		case uint(client.MessageTypeVaultWithdraw):
			// 解析 VaultWithdraw 消息
			var vaultWithdraw client.VaultWithdraw
			if err := msg.DecodeData(&vaultWithdraw); err != nil {
				t.Logf("解析 VaultWithdraw 消息失败: %v", err)
				return err
			}
			// 检查是否是我们要等待的交易
			if vaultWithdraw.TxHash == txHash && uint(messageType) == uint(client.MessageTypeVaultWithdraw) {
				t.Logf("找到匹配的 VaultWithdraw 交易消息: %s", txHash)
				select {
				case messageChan <- &vaultWithdraw:
					// 消息已发送到通道
				default:
					// 通道已满，忽略
				}
			} else {
				t.Logf("VaultWithdraw 消息不匹配: 收到 TxHash=%s, 期望 TxHash=%s", vaultWithdraw.TxHash, txHash)
			}

		case uint(client.MessageTypeTokenTransfer):
			// 解析 TokenTransfer 消息
			var tokenTransfer client.TokenTransfer
			if err := msg.DecodeData(&tokenTransfer); err != nil {
				t.Logf("解析 TokenTransfer 消息失败: %v", err)
				return err
			}
			// 检查是否是我们要等待的交易
			if tokenTransfer.TxHash == txHash && uint(messageType) == uint(client.MessageTypeTokenTransfer) {
				t.Logf("找到匹配的 TokenTransfer 交易消息: %s", txHash)
				select {
				case messageChan <- &tokenTransfer:
					// 消息已发送到通道
				default:
					// 通道已满，忽略
				}
			} else {
				t.Logf("TokenTransfer 消息不匹配: 收到 TxHash=%s, 期望 TxHash=%s", tokenTransfer.TxHash, txHash)
			}
		case uint(client.MessageTypeFundVaultRedemptionRequest):
			var redemptionRequest client.FundRedemptionRequest
			if err := msg.DecodeData(&redemptionRequest); err != nil {
				t.Logf("解析 redemptionRequest 消息失败: %v", err)
				return err
			}
			// 检查是否是我们要等待的交易
			if redemptionRequest.TxHash == txHash && uint(messageType) == uint(client.MessageTypeFundVaultRedemptionRequest) {
				t.Logf("找到匹配的 redemptionRequest 交易消息: %s", txHash)
				select {
				case messageChan <- &redemptionRequest:
					// 消息已发送到通道
				default:
					// 通道已满，忽略
				}
			}
		case uint(client.MessageTypeFundVaultRedemptionRequestCancel):
			var redemptionRequest client.FundRedemptionRequestCancel
			if err := msg.DecodeData(&redemptionRequest); err != nil {
				t.Logf("解析 redemptionRequestCancel 消息失败: %v", err)
				return err
			}
			// 检查是否是我们要等待的交易
			if redemptionRequest.TxHash == txHash && uint(messageType) == uint(client.MessageTypeFundVaultRedemptionRequestCancel) {
				t.Logf("找到匹配的 redemptionRequestCancel 交易消息: %s", txHash)
				select {
				case messageChan <- &redemptionRequest:
					// 消息已发送到通道
				default:
					// 通道已满，忽略
				}
			}
		case uint(client.MessageTypeFundVaultChangeEpoch):
			var redemptionRequest client.FundChangeEpoch
			if err := msg.DecodeData(&redemptionRequest); err != nil {
				t.Logf("解析 FundChangeEpoch 消息失败: %v", err)
				return err
			}
			// 检查是否是我们要等待的交易
			if redemptionRequest.TxHash == txHash && uint(messageType) == uint(client.MessageTypeFundVaultChangeEpoch) {
				t.Logf("找到匹配的 FundChangeEpoch 交易消息: %s", txHash)
				select {
				case messageChan <- &redemptionRequest:
					// 消息已发送到通道
				default:
					// 通道已满，忽略
				}
			}
		case uint(client.MessageTypeFundVaultFinishEpoch):
			var redemptionRequest client.FundFinishEpoch
			if err := msg.DecodeData(&redemptionRequest); err != nil {
				t.Logf("解析 FundFinishEpoch 消息失败: %v", err)
				return err
			}
			// 检查是否是我们要等待的交易
			if redemptionRequest.TxHash == txHash && uint(messageType) == uint(client.MessageTypeFundVaultFinishEpoch) {
				t.Logf("找到匹配的 FundFinishEpoch 交易消息: %s", txHash)
				select {
				case messageChan <- &redemptionRequest:
					// 消息已发送到通道
				default:
					// 通道已满，忽略
				}
			}
		case uint(client.MessageTypeFundVaultRedemptionClaim):
			var redemptionRequest client.FundRedemptionClaim
			if err := msg.DecodeData(&redemptionRequest); err != nil {
				t.Logf("解析 FundVaultRedemptionClaim 消息失败: %v", err)
				return err
			}
			// 检查是否是我们要等待的交易
			if redemptionRequest.TxHash == txHash && uint(messageType) == uint(client.MessageTypeFundVaultRedemptionClaim) {
				t.Logf("找到匹配的 FundVaultRedemptionClaim 交易消息: %s", txHash)
				select {
				case messageChan <- &redemptionRequest:
					// 消息已发送到通道
				default:
					// 通道已满，忽略
				}
			}
		case uint(client.MessageTypeFundVaultAddPrice):
			var redemptionRequest client.FundAddPrice
			if err := msg.DecodeData(&redemptionRequest); err != nil {
				t.Logf("解析 FundAddPrice 消息失败: %v", err)
				return err
			}
			// 检查是否是我们要等待的交易
			if redemptionRequest.TxHash == txHash && uint(messageType) == uint(client.MessageTypeFundVaultAddPrice) {
				t.Logf("找到匹配的 FundAddPrice 交易消息: %s", txHash)
				select {
				case messageChan <- &redemptionRequest:
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

	// 订阅 MQ 主题，使用唯一的 channel 名称
	channelID, err := test.mq.Subscribe(appId, uniqueChannelName, "222", handler)
	if err != nil {
		t.Fatalf("订阅 MQ 失败: %v", err)
	}
	defer test.mq.Unsubscribe(channelID)

	t.Logf("已订阅 MQ 主题 vault_events (channel: %s)，等待消息...", uniqueChannelName)

	// 等待消息，设置超时时间
	timeout := time.After(60 * time.Second)
	select {
	case msg := <-messageChan:
		t.Logf("收到 MQ 消息: %+v", msg)
		return msg
	case <-timeout:
		t.Logf("等待 MQ 消息超时")
		return nil
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

	//t.Logf("调用 /api/v2/transfer/prepare")

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

// compareReceiptWithMQMessage 对比交易回执和MQ消息的一致性
func (test *VaultLaunchIntegrationTest) compareReceiptWithMQMessage(t *testing.T, req *client.RequestTransferPrepareReq, txHash string, mqMessage *client.TokenTransfer) {
	t.Logf("开始对比交易回执和MQ消息的一致性")

	// 获取交易回执
	receipt, err := test.ethClient.TransactionReceipt(context.Background(), common.HexToHash(txHash))
	if err != nil {
		t.Fatalf("获取交易回执失败: %v", err)
	}

	// 验证交易状态
	if receipt.Status != types.ReceiptStatusSuccessful {
		t.Fatalf("交易执行失败，状态码: %d", receipt.Status)
	}

	// 解析回执中的Transfer事件
	contractAddr := common.HexToAddress(*req.TokenAddr)
	from := common.HexToAddress(req.FromAddr)
	to := common.HexToAddress(req.ToAddr)

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

				if logFrom == from && logTo == to {
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
	receiptAmount := new(big.Int).SetBytes(transferEvent.Data)

	// 对比回执事件和MQ消息的数据
	t.Logf("对比回执事件和MQ消息:")
	t.Logf("  回执 - From: %s, To: %s, Amount: %s, TokenAddr: %s",
		from.Hex(), to.Hex(), receiptAmount.String(), contractAddr.Hex())
	t.Logf("  MQ消息 - Sender: %s, Receiver: %s, Amount: %s, TokenAddr: %s",
		mqMessage.Sender, mqMessage.ReceiverAddress, mqMessage.TokenAmount, mqMessage.TokenAddress)

	// 验证发送方地址
	if strings.ToLower(from.Hex()) != strings.ToLower(mqMessage.Sender) {
		t.Errorf("发送方地址不匹配，回执: %s, MQ消息: %s", from.Hex(), mqMessage.Sender)
	}

	// 验证接收方地址
	if strings.ToLower(to.Hex()) != strings.ToLower(mqMessage.ReceiverAddress) {
		t.Errorf("接收方地址不匹配，回执: %s, MQ消息: %s", to.Hex(), mqMessage.ReceiverAddress)
	}

	// 验证转账金额
	mqAmount, ok := new(big.Int).SetString(mqMessage.TokenAmount, 10)
	if !ok {
		t.Fatalf("解析MQ消息转账金额失败: %s", mqMessage.TokenAmount)
	}
	if receiptAmount.Cmp(mqAmount) != 0 {
		t.Errorf("转账金额不匹配，回执: %s, MQ消息: %s", receiptAmount.String(), mqAmount.String())
	}

	// 验证代币地址
	if strings.ToLower(contractAddr.Hex()) != strings.ToLower(mqMessage.TokenAddress) {
		t.Errorf("代币地址不匹配，回执: %s, MQ消息: %s", contractAddr.Hex(), mqMessage.TokenAddress)
	}

	// 验证交易哈希
	if strings.ToLower(txHash) != strings.ToLower(mqMessage.TxHash) {
		t.Errorf("交易哈希不匹配，回执: %s, MQ消息: %s", txHash, mqMessage.TxHash)
	}

	t.Logf("✅ 交易回执和MQ消息对比验证通过")
}

// validateTokenTransferMQMessage 验证 TokenTransfer MQ 消息
func (test *VaultLaunchIntegrationTest) validateTokenTransferMQMessage(t *testing.T, mqMessage *client.TokenTransfer,
	req *client.RequestTransferPrepareReq, txHash string, changed bool) {
	// 验证基础数据
	assert.Equal(t, txHash, mqMessage.TxHash)
	assert.True(t, mqMessage.Success)
	assert.Empty(t, mqMessage.FailReason)
	assert.Equal(t, req.FromAddr, mqMessage.Sender)
	assert.Equal(t, req.ToAddr, mqMessage.ReceiverAddress)
	if changed {
		assert.NotEqual(t, mqMessage.CorrelationId, mqMessage.OnChainCorrelationId)
	} else {
		assert.Equal(t, mqMessage.CorrelationId, mqMessage.OnChainCorrelationId)
	}

	// 验证转账金额
	assert.Equal(t, req.Amount, mqMessage.TokenAmount)

	// 验证代币地址
	if req.TokenAddr != nil {
		assert.Equal(t, *req.TokenAddr, mqMessage.TokenAddress)
	}

	// 验证时间戳
	assert.Greater(t, mqMessage.Ts, int64(0))

	t.Logf("TokenTransfer MQ 消息验证通过:")
	t.Logf("   CorrelationId: %s", mqMessage.CorrelationId)
	t.Logf("   TxHash: %s", mqMessage.TxHash)
	t.Logf("   Success: %t", mqMessage.Success)
	t.Logf("   Sender: %s", mqMessage.Sender)
	t.Logf("   ReceiverAddress: %s", mqMessage.ReceiverAddress)
	t.Logf("   TokenAmount: %s", mqMessage.TokenAmount)
	t.Logf("   TokenAddress: %s", mqMessage.TokenAddress)
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

// callPrepareWithdrawManageFee 调用 prepare_withdraw_fee 接口
func (test *VaultLaunchIntegrationTest) callPrepareWithdrawManageFee(t *testing.T, req *client.RequestVaultWithdrawManagerFeeReq) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 /api/v2/primary/vault/prepare_withdraw_fee")

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/primary/vault/prepare_withdraw_fee", bytes.NewBuffer(reqBody))
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

// validateVaultWithdrawManageFeeMQMessage 验证 VaultWithdrawManageFee MQ 消息
func (test *VaultLaunchIntegrationTest) validateVaultWithdrawManageFeeMQMessage(t *testing.T, mqMessage *client.VaultWithdrawFee, req *client.RequestVaultWithdrawManagerFeeReq, txHash string) {
	// 验证基础数据
	assert.Equal(t, txHash, mqMessage.TxHash)
	assert.True(t, mqMessage.Success)
	assert.Empty(t, mqMessage.FailReason)
	assert.Equal(t, req.Withdrawer, mqMessage.ReceiverAddress)

	// 验证时间戳
	assert.Greater(t, mqMessage.Ts, int64(0))

	t.Logf("VaultWithdrawManageFee MQ 消息验证通过:")
	t.Logf("   CorrelationId: %s", mqMessage.CorrelationId)
	t.Logf("   TxHash: %s", mqMessage.TxHash)
	t.Logf("   Success: %t", mqMessage.Success)
	t.Logf("   ReceiverAddress: %s", mqMessage.ReceiverAddress)
	t.Logf("   manageFeeAmount: %s", mqMessage.AssetTokenAmount)
}

// callPrepareWithdrawManageFee 调用 prepare_withdraw_fee 接口
func (test *VaultLaunchIntegrationTest) callPrepareWithdraw(t *testing.T, req *client.RequestVaultWithdrawAssetReq) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 /api/v2/primary/vault/prepare_withdraw")

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/primary/vault/prepare_withdraw", bytes.NewBuffer(reqBody))
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

// validateVaultWithdrawMQMessage 验证 VaultWithdraw MQ 消息
func (test *VaultLaunchIntegrationTest) validateVaultWithdrawMQMessage(t *testing.T, mqMessage *client.VaultWithdraw, req *client.RequestVaultWithdrawAssetReq, txHash string) {
	// 验证基础数据
	assert.Equal(t, txHash, mqMessage.TxHash)
	assert.True(t, mqMessage.Success)
	assert.Empty(t, mqMessage.FailReason)
	assert.Equal(t, req.Withdrawer, mqMessage.ReceiverAddress)

	// 验证时间戳
	assert.Greater(t, mqMessage.Ts, int64(0))

	t.Logf("VaultWithdraw MQ 消息验证通过:")
	t.Logf("   CorrelationId: %s", mqMessage.CorrelationId)
	t.Logf("   TxHash: %s", mqMessage.TxHash)
	t.Logf("   Success: %t", mqMessage.Success)
	t.Logf("   ReceiverAddress: %s", mqMessage.ReceiverAddress)
	t.Logf("   AssetTokenAmount: %s", mqMessage.AssetTokenAmount)
}

// getNonce 获取账户的当前nonce
func (test *VaultLaunchIntegrationTest) getNonce(t *testing.T, address string) uint64 {
	addr := common.HexToAddress(address)
	nonce, err := test.ethClient.PendingNonceAt(context.Background(), addr)
	require.NoError(t, err, "获取nonce失败")
	t.Logf("账户 %s 当前nonce: %d", address, nonce)
	return nonce
}

// modifyTransactionNonce 修改交易的nonce
func modifyTransactionNonce(tx *types.Transaction, newNonce uint64) *types.Transaction {
	// 创建新的交易，使用新的nonce
	newTx := types.NewTransaction(
		newNonce,
		*tx.To(),
		tx.Value(),
		tx.Gas(),
		tx.GasPrice(),
		tx.Data(),
	)
	return newTx
}

// BalanceManager 余额管理器
type BalanceManager struct {
	test     *VaultLaunchIntegrationTest
	chainId  string
	mockUSDC string
}

// NewBalanceManager 创建余额管理器
func NewBalanceManager(test *VaultLaunchIntegrationTest, chainId, mockUSDC string) *BalanceManager {
	return &BalanceManager{
		test:     test,
		chainId:  chainId,
		mockUSDC: mockUSDC,
	}
}

// RecordUserBalance 记录用户余额
func (bm *BalanceManager) RecordUserBalance(t *testing.T, userAddr string) string {
	balanceReq := &client.RequestBalanceQueryReq{
		ChainId:   client.RequestChainId(bm.chainId),
		UserAddr:  userAddr,
		TokenAddr: &bm.mockUSDC,
		TokenType: client.TokenTypeUSDC,
	}
	balance := bm.test.callTokenBalance(t, balanceReq)
	t.Logf("记录用户 %s 的USDC余额: %s", userAddr, balance)
	return balance
}

// AdjustUserBalanceForTest 为测试调整用户余额，确保满足 amount < 余额 < 2*amount
func (bm *BalanceManager) AdjustUserBalanceForTest(t *testing.T, userAddr, adminAddr, adminPrivateKey, userPrivateKey, amount string) string {
	currentBalance := bm.RecordUserBalance(t, userAddr)

	currentBig, _ := new(big.Int).SetString(currentBalance, 10)
	amountBig, _ := new(big.Int).SetString(amount, 10)
	doubleAmountBig := new(big.Int).Mul(amountBig, big.NewInt(2))

	// 检查余额是否满足条件
	if currentBig.Cmp(amountBig) <= 0 || currentBig.Cmp(doubleAmountBig) >= 0 {
		// 余额不满足条件，需要调整到合适的范围
		// 设置目标余额为 1.5 * amount，确保 amount < 目标余额 < 2*amount
		targetBalance := new(big.Int).Add(amountBig, new(big.Int).Div(amountBig, big.NewInt(2)))

		if currentBig.Cmp(targetBalance) < 0 {
			// 当前余额不足，需要转入
			diff := new(big.Int).Sub(targetBalance, currentBig)
			t.Logf("用户余额不足，需要从admin转入: %s", diff.String())
			bm.transferTokens(t, adminAddr, userAddr, adminPrivateKey, diff.String())
		} else if currentBig.Cmp(targetBalance) > 0 {
			// 当前余额过多，需要转出
			diff := new(big.Int).Sub(currentBig, targetBalance)
			t.Logf("用户余额过多，需要转出到admin: %s", diff.String())
			bm.transferTokens(t, userAddr, adminAddr, userPrivateKey, diff.String())
		}

		// 重新查询调整后的余额
		adjustedBalance := bm.RecordUserBalance(t, userAddr)
		t.Logf("调整后用户的USDC余额: %s", adjustedBalance)
		return adjustedBalance
	}

	return currentBalance
}

// RestoreUserBalance 恢复用户余额到初始状态
func (bm *BalanceManager) RestoreUserBalance(t *testing.T, userAddr, receiverAddr, receiverPrivateKey, initialBalance string) {
	currentBalance := bm.RecordUserBalance(t, userAddr)

	// 如果余额有变化，通过receiver转账恢复
	if currentBalance != initialBalance {
		// 计算需要恢复的金额
		initialBig, _ := new(big.Int).SetString(initialBalance, 10)
		currentBig, _ := new(big.Int).SetString(currentBalance, 10)
		diff := new(big.Int).Sub(initialBig, currentBig)

		if diff.Sign() > 0 {
			// 需要从receiver转回给用户
			t.Logf("恢复用户余额，从 %s 转账 %s 给 %s", receiverAddr, diff.String(), userAddr)
			bm.transferTokens(t, receiverAddr, userAddr, receiverPrivateKey, diff.String())
		}
	}
}

// transferTokens 内部方法：执行代币转账
func (bm *BalanceManager) transferTokens(t *testing.T, fromAddr, toAddr, privateKey, amount string) {
	transferReq := &client.RequestTransferPrepareReq{
		ChainId:   client.RequestChainId(bm.chainId),
		FromAddr:  fromAddr,
		ToAddr:    toAddr,
		TokenAddr: &bm.mockUSDC,
		Amount:    amount,
		TokenType: client.TokenTypeUSDC,
	}

	transferResp := bm.test.callPrepareTokenTransfer(t, transferReq)
	if transferResp != nil && transferResp.TxMsgBase64 != "" {
		tx := &types.Transaction{}
		data, err := base64.StdEncoding.DecodeString(transferResp.TxMsgBase64)
		if err == nil {
			err = tx.UnmarshalBinary(data)
			if err == nil {
				signedTx, err := signTransaction(t, bm.chainId, privateKey, tx)
				if err == nil {
					// 编码为base64
					signedTxData, err := signedTx.MarshalBinary()
					if err == nil {
						signedTxBase64 := base64.StdEncoding.EncodeToString(signedTxData)
						submitResp := bm.test.callSubmitTx(t, &client.RequestSubmitReq{
							ChainId:      client.CommonChainID(transferReq.ChainId),
							Sender:       transferReq.FromAddr,
							TxMsgBase64:  transferResp.TxMsgBase64,
							SignTxBase64: signedTxBase64,
						})
						if submitResp != nil && submitResp.TxHash != "" {
							t.Logf("✅ 转账交易提交成功，TxHash: %s", submitResp.TxHash)
							// 等待交易确认
							time.Sleep(5 * time.Second)
						}
					}
				}
			}
		}
	}
}

type FundVaultRedemptionRequestApprove struct {
	ChainId      string `json:"chain_id"`
	Investor     string `json:"investor"`
	VaultAddress string `json:"vault_address"`
	Amount       string `json:"amount"`
}

// callPrepareFundRedemptionApprove 调用 prepare_fund_redeem_approve 接口
func (test *VaultLaunchIntegrationTest) callPrepareFundRedemptionApprove(t *testing.T, req *FundVaultRedemptionRequestApprove) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/fund/prepare_fund_redeem_approve", bytes.NewBuffer(reqBody))
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

	t.Logf("prepare_fund_redeem_approve 响应: %+v", apiResp)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp PrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

type FundVaultFinishEpochApprove struct {
	ChainId        string `json:"chain_id"`
	SettlerAddress string `json:"settler_address"`
	//vault地址
	VaultAddress string `json:"vault_address"`
	AssetAmount  string `json:"asset_amount"`
}

func (test *VaultLaunchIntegrationTest) callPrepareFundFinishEpochApprove(t *testing.T, req *FundVaultFinishEpochApprove) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/fund/prepare_fund_finish_epoch_approve", bytes.NewBuffer(reqBody))
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

	t.Logf("prepare_fund_finish_epoch_approve 响应: %+v", apiResp)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp PrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

type FundVaultFinishEpoch struct {
	ChainId        string `json:"chain_id"`
	VaultAddress   string `json:"vault_address"`
	SettlerAddress string `json:"settler_address"` // 结算人地址
	EpochId        string `json:"epoch_id"`
	AssetAmount    string `json:"asset_amount"` //金额
	Signature      string `json:"signature"`    //drds签名
}

func (test *VaultLaunchIntegrationTest) callPrepareFundFinishEpoch(t *testing.T, req *FundVaultFinishEpoch) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/fund/prepare_finish_epoch", bytes.NewBuffer(reqBody))
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

	t.Logf("prepare_finish_epoch 响应: %+v", apiResp)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp PrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

// FundRedeemRequest fund Vault 赎回请求
type FundRedeemRequest struct {
	ChainId         string `json:"chain_id"`
	VaultAddress    string `json:"vault_address"`
	FundTokenAmount string `json:"fund_token_amount"`
	UserAddr        string `json:"user_addr"` // 用户地址
}

// callPrepareFundRedemptionRequest 调用 prepare_fund_redeem 接口
func (test *VaultLaunchIntegrationTest) callPrepareFundRedemptionRequest(t *testing.T, req *FundRedeemRequest) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/fund/prepare_fund_redeem", bytes.NewBuffer(reqBody))
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

	t.Logf("prepare_fund_redeem 响应: %+v", apiResp)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp PrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

// FundRedeemRequest fund Vault 赎回请求
type FundRedeemRequestCancel struct {
	ChainId      string `json:"chain_id"`
	VaultAddress string `json:"vault_address"`
	UserAddr     string `json:"user_addr"` // 用户地址
}

func (test *VaultLaunchIntegrationTest) callPrepareFundCancelRedemptionRequest(t *testing.T, req *FundRedeemRequestCancel) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/fund/prepare_cancel_redeem", bytes.NewBuffer(reqBody))
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

	t.Logf("prepare_cancel_redeem 响应: %+v", apiResp)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp PrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

type FundVaultChangeEpoch struct {
	ChainId        string `json:"chain_id"`
	VaultAddress   string `json:"vault_address"`
	ManagerAddress string `json:"manager_address"` // 用户地址
}

func (test *VaultLaunchIntegrationTest) callPrepareFundChangeEpoch(t *testing.T, req *FundVaultChangeEpoch) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/fund/prepare_change_epoch", bytes.NewBuffer(reqBody))
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

	t.Logf("prepare_change_epoch 响应: %+v", apiResp)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp PrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

type FundVaultClaimRedemptionRequest struct {
	ChainId      string `json:"chain_id"`
	VaultAddress string `json:"vault_address"`
	UserAddr     string `form:"user_addr"` // 用户地址
	EpochId      string `json:"epoch_id"`
}

func (test *VaultLaunchIntegrationTest) callPrepareFundClaimRedemption(t *testing.T, req *FundVaultClaimRedemptionRequest) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/fund/prepare_claim_redemption", bytes.NewBuffer(reqBody))
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

	t.Logf("prepare_claim_redemption 响应: %+v", apiResp)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp PrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

type FundVaultAddPriceRequest struct {
	ChainId      string `json:"chain_id"`
	VaultAddress string `json:"vault_address"`
	Price        string `json:"price"`
	PriceFeeder  string `json:"price_feeder"`
}

func (test *VaultLaunchIntegrationTest) callPrepareFundVaultAddPrice(t *testing.T, req *FundVaultAddPriceRequest) *PrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("POST", test.baseURL+"/api/v2/fund/prepare_add_fund_price", bytes.NewBuffer(reqBody))
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

	t.Logf("prepare_add_fund_price 响应: %+v", apiResp)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp PrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

type FundVaultPriceQueryRequest struct {
	ChainId      string `form:"chain_id" url:"chain_id"`
	VaultAddress string `form:"vault_address" url:"vault_address"`
	RoundId      string `form:"round_id" url:"round_id"`
}
type FundVaultRoundPriceInfoRsp struct {
	RoundId  string `json:"round_id"`
	Price    string `json:"price"`
	Decimals int    `json:"decimals"`
	Ts       int    `json:"ts"` //秒级时间戳
}

func (test *VaultLaunchIntegrationTest) callQueryFundVaultPrice(t *testing.T, req *FundVaultPriceQueryRequest) *FundVaultRoundPriceInfoRsp {
	// 创建请求
	v, err := query.Values(req)
	require.NoError(t, err)

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("GET", test.baseURL+"/api/v2/fund/get_round_price?"+v.Encode(), nil)
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

	t.Logf("get_round_price 响应: %+v", apiResp)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp FundVaultRoundPriceInfoRsp
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)
	t.Logf("callQueryFundVaultPrice 查询成功:")
	t.Logf("   RoundId: %s", prepareResp.RoundId)
	t.Logf("   Price: %s", prepareResp.Price)
	t.Logf("   TimeStamp: %d", prepareResp.Ts)
	t.Logf("   Decimals: %d", prepareResp.Decimals)
	return &prepareResp
}

type QueryPendingClaimReq struct {
	ChainId  string `form:"chain_id" url:"chain_id"`   // 链ID
	Vault    string `form:"vault" url:"vault"`         // Vault
	UserAddr string `form:"user_addr" url:"user_addr"` // 用户地址
	EpochId  string `form:"epoch_id" url:"epoch_id"`   //链上赎回周期ID
}
type FundUserPendingClaimRsp struct {
	AssetAmount string `json:"asset_amount"` //U的数量
}

func (test *VaultLaunchIntegrationTest) callQueryUserPendingClaim(t *testing.T, req *QueryPendingClaimReq) *FundUserPendingClaimRsp {
	// 创建请求
	v, err := query.Values(req)
	require.NoError(t, err)

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("GET", test.baseURL+"/api/v2/fund/user_pending_claim?"+v.Encode(), nil)
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

	t.Logf("user_pending_claim 响应: %+v", apiResp)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp FundUserPendingClaimRsp
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)
	t.Logf("callQueryUserPendingClaim 查询成功:")
	t.Logf("   EpochId: %s", req.EpochId)
	t.Logf("   User: %s", req.UserAddr)
	t.Logf("   AssetAmount: %s", prepareResp.AssetAmount)
	return &prepareResp
}

type QueryFundEpochDataReq struct {
	ChainId string `form:"chain_id" url:"chain_id"` // 链ID
	Vault   string `form:"vault" url:"vault"`       // Vault
	EpochId string `form:"epoch_id" url:"epoch_id"` //链上赎回周期ID
}
type FundEpochDataResp struct {
	TotalShares           string `json:"total_shares"`
	TotalRedemptionAssets string `json:"total_redemption_assets"`
	TotalClaimedAssets    string `json:"total_claimed_assets"`
	EpochStatus           string `json:"epoch_status"`
}

func (test *VaultLaunchIntegrationTest) callQueryEpochData(t *testing.T, req *QueryFundEpochDataReq) *FundEpochDataResp {
	// 创建请求
	v, err := query.Values(req)
	require.NoError(t, err)

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("GET", test.baseURL+"/api/v2/fund/epoch_data?"+v.Encode(), nil)
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

	t.Logf("epoch_data 响应: %+v", apiResp)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp FundEpochDataResp
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)
	t.Logf("callQueryEpochData 查询成功:")
	t.Logf("   EpochId: %s", req.EpochId)
	t.Logf("   TotalShares: %s", prepareResp.TotalShares)
	t.Logf("   TotalRedemptionAssets: %s", prepareResp.TotalRedemptionAssets)
	t.Logf("   TotalClaimedAssets: %s", prepareResp.TotalClaimedAssets)
	t.Logf("   EpochStatus: %s", prepareResp.EpochStatus)
	return &prepareResp
}

type QueryFundUserRedemptionInfoReq struct {
	ChainId  string `form:"chain_id" url:"chain_id"`   // 链ID
	Vault    string `form:"vault" url:"vault"`         // Vault
	UserAddr string `form:"user_addr" url:"user_addr"` // 用户地址
	EpochId  string `form:"epoch_id" url:"epoch_id"`   //链上赎回周期ID
}
type FundUserRedemptionInfoRsp struct {
	RequestShares        string `json:"request_shares"`
	ClaimShares          string `json:"claim_shares"`
	ClaimAssets          string `json:"claim_assets"`
	LastRequestTimeStamp int    `json:"last_request_time_stamp"`
	LastClaimTimeStamp   int    `json:"last_claim_time_stamp"`
}

func (test *VaultLaunchIntegrationTest) callQueryUserRedemptionInfo(t *testing.T, req *QueryFundUserRedemptionInfoReq) *FundUserRedemptionInfoRsp {
	// 创建请求
	v, err := query.Values(req)
	require.NoError(t, err)

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("GET", test.baseURL+"/api/v2/fund/user_redemption_info?"+v.Encode(), nil)
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

	t.Logf("user_redemption_info 响应: %+v", apiResp)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp FundUserRedemptionInfoRsp
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)
	t.Logf("UserRedemptionInfo 查询成功:")
	t.Logf("   EpochId: %s", req.EpochId)
	t.Logf("   RequestShares: %s", prepareResp.RequestShares)
	t.Logf("   ClaimShares: %s", prepareResp.ClaimShares)
	t.Logf("   ClaimAssets: %s", prepareResp.ClaimAssets)
	t.Logf("   LastRequestTimeStamp: %d", prepareResp.LastRequestTimeStamp)
	t.Logf("   LastClaimTimeStamp: %d", prepareResp.LastClaimTimeStamp)
	return &prepareResp
}

type FundVaultCurrentEpochIdRequest struct {
	ChainId string `form:"chain_id" url:"chain_id"`
	Vault   string `form:"vault" url:"vault"`
}
type FundCurrentEpochIdRsp struct {
	CurrentEpochId string `json:"current_epoch_id"`
}

func (test *VaultLaunchIntegrationTest) callQueryCurrentEpochId(t *testing.T, req *FundVaultCurrentEpochIdRequest) *FundCurrentEpochIdRsp {
	// 创建请求
	v, err := query.Values(req)
	require.NoError(t, err)

	// 创建 HTTP 请求
	httpReq, err := http.NewRequest("GET", test.baseURL+"/api/v2/fund/current_epoch_id?"+v.Encode(), nil)
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

	t.Logf("current_epoch_id 响应: %+v", apiResp)

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp FundCurrentEpochIdRsp
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)
	t.Logf("CurrentEpochId 查询成功:")
	t.Logf("   Vault: %s", req.Vault)
	t.Logf("   CurrentEpochId: %s", prepareResp.CurrentEpochId)
	return &prepareResp
}

// validateVaultInvestMQMessage 验证 VaultInvest MQ 消息
func (test *VaultLaunchIntegrationTest) validateFundVaultRedemptionRequestMQMessage(t *testing.T, mqMessage *client.FundRedemptionRequest, req *FundRedeemRequest, txHash string) {
	// 验证基础数据
	assert.Equal(t, txHash, mqMessage.TxHash)
	assert.True(t, mqMessage.Success)
	assert.Empty(t, mqMessage.FailReason)
	assert.Equal(t, req.UserAddr, mqMessage.Sender)
	assert.Equal(t, req.FundTokenAmount, mqMessage.ShareAmount)
	// 验证时间戳
	assert.Greater(t, mqMessage.Ts, int64(0))

	t.Logf("FundVaultRedemptionRequest MQ 消息验证通过:")
	t.Logf("   CorrelationId: %s", mqMessage.CorrelationId)
	t.Logf("   TxHash: %s", mqMessage.TxHash)
	t.Logf("   Success: %t", mqMessage.Success)
	t.Logf("   Sender: %s", mqMessage.Sender)
	t.Logf("   VaultTokenAmount: %s", mqMessage.ShareAmount)
}
func (test *VaultLaunchIntegrationTest) validateFundVaultRedemptionRequestCancelMQMessage(t *testing.T, mqMessage *client.FundRedemptionRequestCancel, req *FundRedeemRequestCancel, txHash string) {
	// 验证基础数据
	assert.Equal(t, txHash, mqMessage.TxHash)
	assert.True(t, mqMessage.Success)
	assert.Empty(t, mqMessage.FailReason)
	assert.Equal(t, req.UserAddr, mqMessage.Sender)
	// 验证时间戳
	assert.Greater(t, mqMessage.Ts, int64(0))

	t.Logf("FundVaultRedemptionRequestCancel MQ 消息验证通过:")
	t.Logf("   CorrelationId: %s", mqMessage.CorrelationId)
	t.Logf("   TxHash: %s", mqMessage.TxHash)
	t.Logf("   Success: %t", mqMessage.Success)
	t.Logf("   Sender: %s", mqMessage.Sender)
	t.Logf("   VaultTokenAmount: %s", mqMessage.ShareAmount)
}

func (test *VaultLaunchIntegrationTest) validateFundVaultAddPriceMQMessage(t *testing.T, mqMessage *client.FundAddPrice, req *FundVaultAddPriceRequest, txHash string) {
	// 验证基础数据
	assert.Equal(t, txHash, mqMessage.TxHash)
	assert.True(t, mqMessage.Success)
	assert.Empty(t, mqMessage.FailReason)
	assert.Equal(t, req.PriceFeeder, mqMessage.Sender)
	// 验证时间戳
	assert.Greater(t, mqMessage.Ts, int64(0))

	t.Logf("FundVaultAddPrice MQ 消息验证通过:")
	t.Logf("   CorrelationId: %s", mqMessage.CorrelationId)
	t.Logf("   TxHash: %s", mqMessage.TxHash)
	t.Logf("   Success: %t", mqMessage.Success)
	t.Logf("   Sender: %s", mqMessage.Sender)
	t.Logf("   Price: %s", mqMessage.Price)
	t.Logf("   LatestRoundId: %s", mqMessage.LatestRoundId)
}

func (test *VaultLaunchIntegrationTest) validateFundVaultChangeEpochMQMessage(t *testing.T, mqMessage *client.FundChangeEpoch, req *FundVaultChangeEpoch, txHash string) {
	// 验证基础数据
	assert.Equal(t, txHash, mqMessage.TxHash)
	assert.True(t, mqMessage.Success)
	assert.Empty(t, mqMessage.FailReason)
	assert.Equal(t, req.ManagerAddress, mqMessage.Sender)
	// 验证时间戳
	assert.Greater(t, mqMessage.Ts, int64(0))

	t.Logf("FundVaultChangeEpoch MQ 消息验证通过:")
	t.Logf("   CorrelationId: %s", mqMessage.CorrelationId)
	t.Logf("   TxHash: %s", mqMessage.TxHash)
	t.Logf("   Success: %t", mqMessage.Success)
	t.Logf("   Sender: %s", mqMessage.Sender)
	t.Logf("   EpochId: %s", mqMessage.EpochId)
}
func (test *VaultLaunchIntegrationTest) validateFundVaultFinishEpochMQMessage(t *testing.T, mqMessage *client.FundFinishEpoch, req *FundVaultFinishEpoch, txHash string) {
	// 验证基础数据
	assert.Equal(t, txHash, mqMessage.TxHash)
	assert.True(t, mqMessage.Success)
	assert.Empty(t, mqMessage.FailReason)
	assert.Equal(t, req.SettlerAddress, mqMessage.Sender)
	assert.Equal(t, req.EpochId, mqMessage.EpochId)
	assert.Equal(t, req.AssetAmount, mqMessage.AssetAmount)
	// 验证时间戳
	assert.Greater(t, mqMessage.Ts, int64(0))

	t.Logf("FundVaultFinishEpoch MQ 消息验证通过:")
	t.Logf("   CorrelationId: %s", mqMessage.CorrelationId)
	t.Logf("   TxHash: %s", mqMessage.TxHash)
	t.Logf("   Success: %t", mqMessage.Success)
	t.Logf("   Sender: %s", mqMessage.Sender)
	t.Logf("   AssetAmount: %s", mqMessage.AssetAmount)
}

func (test *VaultLaunchIntegrationTest) validateFundVaultClaimRedemptionMQMessage(t *testing.T, mqMessage *client.FundRedemptionClaim, req *FundVaultClaimRedemptionRequest, txHash string) {
	// 验证基础数据
	assert.Equal(t, txHash, mqMessage.TxHash)
	assert.True(t, mqMessage.Success)
	assert.Empty(t, mqMessage.FailReason)
	assert.Equal(t, req.UserAddr, mqMessage.Sender)
	assert.Equal(t, req.EpochId, mqMessage.EpochId)
	// 验证时间戳
	assert.Greater(t, mqMessage.Ts, int64(0))

	t.Logf("FundVaultClaimRedemption MQ 消息验证通过:")
	t.Logf("   CorrelationId: %s", mqMessage.CorrelationId)
	t.Logf("   TxHash: %s", mqMessage.TxHash)
	t.Logf("   Success: %t", mqMessage.Success)
	t.Logf("   Sender: %s", mqMessage.Sender)
	t.Logf("   EpochId: %s", mqMessage.EpochId)
	t.Logf("   AssetAmount: %s", mqMessage.AssetAmount)
	t.Logf("   ShareAmount: %s", mqMessage.ShareAmount)
}