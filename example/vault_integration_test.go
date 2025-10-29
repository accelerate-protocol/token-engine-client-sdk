package example

import (
	"encoding/base64"
	"fmt"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/require"

	client "github.com/accelerate-protocol/token-engine-client-sdk"
)

var (
	config Config

	serverUrl       string
	adminPrivateKey string
	chainId         string
	admin           string
	users           []Signer
	mockUSDC        string
)

func init() {
	// 加载配置文件
	data, err := os.ReadFile("priv.toml")
	if err != nil {
		panic(fmt.Sprintf("Failed to read priv.toml: %v", err))
	}

	if err := toml.Unmarshal(data, &config); err != nil {
		panic(fmt.Sprintf("Failed to parse priv.toml: %v", err))
	}

	// 从配置中初始化变量
	serverUrl = config.Server.URL
	adminPrivateKey = config.Admin.PrivateKey
	chainId = config.Blockchain.ChainID
	admin = config.Admin.Address
	users = config.Users
	mockUSDC = config.Blockchain.MockUSDC
}

// encodeTransactionToBase64 将签名后的交易编码为base64字符串
func encodeTransactionToBase64(t *testing.T, signedTx *types.Transaction) string {
	signedTxBytes, err := signedTx.MarshalBinary()
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(signedTxBytes)
}

// 添加发行人白名单
func TestAddVaultDeployerIntegration(t *testing.T) {
	// 创建测试实例，连接本地 token-engine 服务
	test := NewVaultLaunchIntegrationTest(serverUrl, t)
	t.Logf("开始 AddDeployer 集成测试")
	req := &client.RequestAddVaultDeployerWhiteListReq{
		ChainId:         client.CommonChainID(chainId),
		DeployerAddress: "0x318AC2c326700F9245BB2673B0885E4358dc2977",
		OwnerAddress:    admin,
	}
	prepareResp := test.callPrepareAddDeployer(t, req)
	require.NotNil(t, prepareResp)
	require.NotEmpty(t, prepareResp.TxMsgBase64)

	t.Logf("准备交易成功，CorrelationId: %s", prepareResp.CorrelationId)
	tx := &types.Transaction{}
	data, err := base64.StdEncoding.DecodeString(prepareResp.TxMsgBase64)
	require.NoError(t, err)
	err = tx.UnmarshalBinary(data)
	require.NoError(t, err)
	// 3. 签名交易
	signedTx, err := signTransaction(t, chainId, adminPrivateKey, tx)
	require.NoError(t, err)

	signedTxBytes, err := signedTx.MarshalBinary()
	require.NoError(t, err)
	signedTxBase64 := base64.StdEncoding.EncodeToString(signedTxBytes)

	t.Logf("交易签名成功，准备提交交易")
	// 4. 调用 /api/v1/common/submit_tx 接口提交交易
	submitResp := test.callSubmitTx(t, &client.RequestSubmitReq{
		ChainId:      req.ChainId,
		Sender:       req.OwnerAddress,
		TxMsgBase64:  prepareResp.TxMsgBase64,
		SignTxBase64: signedTxBase64,
	})
	require.NotNil(t, submitResp)
	require.NotEmpty(t, submitResp.TxHash)

	t.Logf("交易提交成功，TxHash: %s", submitResp.TxHash)
}

// TestVaultIntegration VaultLaunch 集成测试
func TestVaultSuccessIntegration(t *testing.T) {
	// 创建测试实例，连接本地 token-engine 服务
	test := NewVaultLaunchIntegrationTest(serverUrl, t)
	t.Logf("开始 VaultLaunch 集成测试")
	t.Logf("===============阶段一：Launch Vault====================")
	mqMessage := createVault(t, test, 24*time.Hour)
	t.Logf("===============阶段二：User Invest ====================")
	amount := parseUsd(10000) // 10000 USDC

	deposit(t, test, mqMessage.VaultAddress, amount, users[0], users[0])
	t.Logf("✅ User Invest 集成测试通过")

	t.Logf("===============阶段三：Drds admin Dividend====================")
	dividend(t, test, mqMessage.VaultAddress, parseUsd(1000), 0) // 1000 USDC
	t.Logf("✅ Drds admin Dividend 集成测试通过")
	t.Logf("===============阶段四：User Claim ====================")
	claim(t, test, mqMessage.VaultAddress, users[0])
	t.Logf("✅ User Claim 集成测试通过")
}

// TestVaultDeposit VaultDeposit 集成测试
func TestVaultDeposit(t *testing.T) {
	// 创建测试实例，连接本地 token-engine 服务
	test := NewVaultLaunchIntegrationTest(serverUrl, t)

	testCases := []struct {
		name      string
		vaultType client.CommonVaultType
		sender    Signer
		receiver  Signer
		amount    string
	}{
		{
			name:     "RBF模式下，单链支付，用户1质押给自己",
			sender:   users[0],
			receiver: users[0],
			amount:   parseUsd(1000), // 1000 USDC
		},
		{
			name:     "RBF模式下，多链支付,用户1(多链账户)质押给用户2",
			sender:   users[0],
			receiver: users[1],
			amount:   parseUsd(1000), // 1000 USDC
		},
		{
			name:      "Fund模式下，单链支付，用户1质押给自己",
			sender:    users[0],
			receiver:  users[0],
			amount:    parseUsd(1000), // 1000 USDC
			vaultType: client.VaultTypeFund,
		},
		{
			name:      "Fund模式下，多链支付,用户1(多链账户)质押给用户2",
			sender:    users[0],
			receiver:  users[1],
			amount:    parseUsd(1000), // 1000 USDC
			vaultType: client.VaultTypeFund,
		},
	}

	for _, tc := range testCases {
		t.Logf("开始执行测试用例: %s", tc.name)
		// 1. 创建 Vault, 只测试质押功能，募集期设置为24小时
		var vaultMsg *client.VaultLaunch
		switch tc.vaultType {
		case client.VaultTypeFund:
			vaultMsg = createVault(t, test, 24*time.Hour, withVaultType(client.VaultTypeFund))
		default:
			vaultMsg = createVault(t, test, 24*time.Hour)
		}
		vaultAddr := vaultMsg.VaultAddress
		deposit(t, test, vaultAddr, tc.amount, tc.sender, tc.receiver)
	}
}

func TestVaultRedeem(t *testing.T) {
	// 创建测试实例，连接本地 token-engine 服务
	test := NewVaultLaunchIntegrationTest(serverUrl, t)

	testCases := []struct {
		name   string
		signer Signer
	}{
		{
			name:   "单链用户1赎回",
			signer: users[0],
		},
		{
			name:   "多链用户2赎回",
			signer: users[1],
		},
	}
	for _, tc := range testCases {
		t.Logf("开始执行测试用例: %s", tc.name)
		// 1. 创建 Vault, 只测试赎回功能，募集期设置为1分钟
		vaultMsg := createVault(t, test, 1*time.Minute)
		vaultAddr := vaultMsg.VaultAddress
		amount := parseUsd(100) // 100 USDC (6位精度)
		resp := deposit(t, test, vaultAddr, amount, tc.signer, tc.signer)
		vltAmount := resp.VaultTokenAmount
		depositAmount := resp.AssetTokenAmount
		// 2. 等待募集期结束
		t.Logf("等待募集期结束...")
		time.Sleep(70 * time.Second)
		// 3. 执行 redeem 测试(全部赎回)
		redeemAmount := redeem(t, test, vaultAddr, tc.signer, vltAmount)
		require.Equal(t, depositAmount, redeemAmount, "赎回金额与质押金额不符")
		t.Logf("✅ VaultRedeem 集成测试通过，赎回金额: %s USDC", redeemAmount)
	}
}

func TestVaultOffchainDeposit(t *testing.T) {
	// 创建测试实例，连接本地 token-engine 服务
	test := NewVaultLaunchIntegrationTest(serverUrl, t)

	testCases := []struct {
		name     string
		receiver Signer
		amount   string
	}{
		{
			name:     "管理员质押给用户1",
			receiver: users[0],
			amount:   parseUsd(1000), // 1000 USDC
		},
	}
	for _, tc := range testCases {
		t.Logf("开始执行测试用例: %s", tc.name)
		// 1. 创建 Vault, 只测试质押功能，募集期设置为24小时
		vaultMsg := createVault(t, test, 24*time.Hour)
		vaultAddr := vaultMsg.VaultAddress
		offchainDeposit(t, test, vaultAddr, tc.amount, tc.receiver)

		t.Logf("✅ VaultOffchainDeposit 集成测试完成")
	}
}

func TestVaultWithdrawManageFee(t *testing.T) {
	// 创建测试实例，连接本地 token-engine 服务
	test := NewVaultLaunchIntegrationTest(serverUrl, t)

	t.Logf("开始执行测试用例: VaultWithdrawManageFee")
	// 1. 创建 Vault
	vaultMsg := createVault(t, test, 24*time.Hour)
	vaultAddr := vaultMsg.VaultAddress
	amount := parseUsd(10000) // 10000 USDC (6位精度)，打满,确保融资成功
	t.Logf("amount: %s", amount)
	deposit(t, test, vaultAddr, amount, users[0], users[0])

	// 3. 管理员提取管理费
	withdrawManageFee(t, test, vaultAddr)
	t.Logf("✅ VaultWithdrawManageFee 集成测试完成")
}

func TestVaultWithdraw(t *testing.T) {
	// 创建测试实例，连接本地 token-engine 服务
	test := NewVaultLaunchIntegrationTest(serverUrl, t)

	t.Logf("开始执行测试用例: VaultWithdraw")
	// 1. 创建 Vault
	vaultMsg := createVault(t, test, 24*time.Hour)
	vaultAddr := vaultMsg.VaultAddress
	amount := parseUsd(10000) // 10000 USDC (6位精度)，打满,确保融资成功
	deposit(t, test, vaultAddr, amount, users[0], users[0])

	// 3. 管理员提取资产
	withdraw(t, test, vaultAddr)
	t.Logf("✅ VaultWithdraw 集成测试完成")
}

func TestUnpauseToken(t *testing.T) {
	// 创建测试实例，连接本地 token-engine 服务
	test := NewVaultLaunchIntegrationTest(serverUrl, t)
	t.Logf("开始 UnpauseToken 集成测试")
	// 1. 创建 Vault
	vaultMsg := createVault(t, test, 24*time.Hour)
	// 2. deposit
	vaultAddr := vaultMsg.VaultAddress
	amount := parseUsd(10000) // 10000 USDC (6位精度)
	deposit(t, test, vaultAddr, amount, users[0], users[0])
	// 3. pause token
	unPauseToken(t, test, vaultMsg.VaultAddress)
}

func TestVaultDividend(t *testing.T) {
	// 创建测试实例，连接本地 token-engine 服务
	test := NewVaultLaunchIntegrationTest(serverUrl, t)

	t.Logf("开始执行测试用例: VaultDividend")
	// 1. 创建 Vault
	vaultMsg := createVault(t, test, 24*time.Hour)
	vaultAddr := vaultMsg.VaultAddress
	amount := parseUsd(10000) // 10000 USDC (6位精度)，打满,确保融资成功
	deposit(t, test, vaultAddr, amount, users[0], users[0])

	// 执行 dividend 测试
	dividend(t, test, vaultAddr, amount, 0)

	t.Logf("✅ VaultDividend 集成测试完成")
}

func TestVaultClaim(t *testing.T) {
	// 创建测试实例，连接本地 token-engine 服务
	test := NewVaultLaunchIntegrationTest(serverUrl, t)

	testCases := []struct {
		name   string
		signer Signer
	}{
		{
			name:   "用户1领取分红",
			signer: users[0],
		},
	}
	for _, tc := range testCases {
		t.Logf("开始执行测试用例: %s", tc.name)
		// 1. 创建 Vault
		vaultMsg := createVault(t, test, 24*time.Hour)
		vaultAddr := vaultMsg.VaultAddress
		amount := parseUsd(10000) // 10000 USDC (6位精度)，打满,确保融资成功
		deposit(t, test, vaultAddr, amount, tc.signer, tc.signer)

		// 2. 管理员派息
		dividendAmount := parseUsd(100) // 100 USDC
		dividend(t, test, vaultAddr, dividendAmount, 0)
		// 4. 执行 claim 测试
		claim(t, test, vaultAddr, tc.signer)

		t.Logf("✅ VaultClaim 集成测试通过")
	}

	t.Logf("✅ VaultClaim 集成测试完成")
}

func TestErc20Approve(t *testing.T) {
	test := NewVaultLaunchIntegrationTest(serverUrl, t)
	// 1. 准备 approve 请求
	approveReq := &client.RequestApprovePrepareReq{
		ChainId:     client.RequestChainId(chainId),
		Amount:      parseUsd(1), // 1 USDC
		FromAddr:    admin,
		TokenAddr:   mockUSDC,
		SpenderAddr: users[1].Address,
	}
	// 2. 调用 /api/v2/common/prepare_approve 接口
	approveResp := test.callPrepareTokenApprove(t, approveReq)
	require.NotNil(t, approveResp)
	require.NotEmpty(t, approveResp.TxMsgBase64)
	t.Logf("准备交易成功，CorrelationId: %s", approveResp.CorrelationId)
	tx := &types.Transaction{}
	data, err := base64.StdEncoding.DecodeString(approveResp.TxMsgBase64)
	require.NoError(t, err)
	err = tx.UnmarshalBinary(data)
	require.NoError(t, err)
	// 3. 签名交易
	signedTx, err := signTransaction(t, string(approveReq.ChainId), adminPrivateKey, tx)
	require.NoError(t, err)

	t.Logf("交易签名成功，准备提交交易")
	// 4. 调用 /api/v1/common/submit_tx 接口提交交易
	submitResp := test.callSubmitTx(t, &client.RequestSubmitReq{
		ChainId:      client.CommonChainID(approveReq.ChainId),
		Sender:       approveReq.FromAddr,
		TxMsgBase64:  approveResp.TxMsgBase64,
		SignTxBase64: encodeTransactionToBase64(t, signedTx),
	})
	require.NotNil(t, submitResp)
	require.NotEmpty(t, submitResp.TxHash)
	t.Logf("交易提交成功，TxHash: %s", submitResp.TxHash)
	// 6. 验证 token approve 回执
	test.validateTokenApproveReceipt(t, approveReq, submitResp.TxHash)
	t.Logf("✅ Erc20Approve 集成测试通过")
}

func TestErc20Transfer(t *testing.T) {
	test := NewVaultLaunchIntegrationTest(serverUrl, t)
	testCases := []struct {
		name      string
		sender    Signer
		amount    string
		receiver  string
		TokenType client.RequestTokenType
	}{
		{
			name: "usdc转账，admin转账给用户2",
			sender: Signer{
				Address:    admin,
				PrivateKey: adminPrivateKey,
			},
			amount:    parseUsd(1), // 1 USDC
			receiver:  users[1].Address,
			TokenType: client.TokenTypeUSDC,
		},
		{
			name: "vault token转账，admin转账给用户2",
			sender: Signer{
				Address:    admin,
				PrivateKey: adminPrivateKey,
			},
			amount:    parseUsd(1), // 1 vlt
			receiver:  users[1].Address,
			TokenType: client.TokenTypeVaultToken,
		},
	}
	for _, tc := range testCases {
		t.Logf("开始执行测试用例: %s", tc.name)
		// 1. 准备 transfer 请求
		var (
			tokenAddr string
		)
		switch tc.TokenType {
		case client.TokenTypeUSDC:
			tokenAddr = mockUSDC
		case client.TokenTypeVaultToken:
			// 先创建一个 vault
			vaultMsg := createVault(t, test, 24*time.Hour)
			// 给 admin 质押一些 vault token，确保融资完成
			amount := parseUsd(10000) // 10000 USDC (6位精度)
			deposit(t, test, vaultMsg.VaultAddress, amount, tc.sender, tc.sender)
			unPauseToken(t, test, vaultMsg.VaultAddress)
			tokenAddr = vaultMsg.VaultTokenAddress
		default:
			require.Fail(t, "不支持的 TokenType")
		}

		transferReq := &client.RequestTransferPrepareReq{
			ChainId:   client.RequestChainId(chainId),
			FromAddr:  tc.sender.Address,
			ToAddr:    tc.receiver,
			TokenAddr: &tokenAddr,
			Amount:    tc.amount,
			TokenType: tc.TokenType,
		}
		// 2. 调用 /api/v2/common/prepare_transfer 接口
		transferResp := test.callPrepareTokenTransfer(t, transferReq)
		require.NotNil(t, transferResp)
		require.NotEmpty(t, transferResp.TxMsgBase64)
		t.Logf("准备交易成功，CorrelationId: %s", transferResp.CorrelationId)
		tx := &types.Transaction{}
		data, err := base64.StdEncoding.DecodeString(transferResp.TxMsgBase64)
		require.NoError(t, err)
		err = tx.UnmarshalBinary(data)
		require.NoError(t, err)
		// 3. 签名交易
		signedTx, err := signTransaction(t, string(transferReq.ChainId), tc.sender.PrivateKey, tx)
		require.NoError(t, err)

		// 编码为base64
		signedTxBytes, err := signedTx.MarshalBinary()
		require.NoError(t, err)
		signedTxBase64 := base64.StdEncoding.EncodeToString(signedTxBytes)

		t.Logf("交易签名成功，准备提交交易")
		// 4. 调用 /api/v1/common/submit_tx 接口提交交易
		submitResp := test.callSubmitTx(t, &client.RequestSubmitReq{
			ChainId:      client.CommonChainID(transferReq.ChainId),
			Sender:       transferReq.FromAddr,
			TxMsgBase64:  transferResp.TxMsgBase64,
			SignTxBase64: signedTxBase64,
		})
		require.NotNil(t, submitResp)
		require.NotEmpty(t, submitResp.TxHash)
		t.Logf("交易提交成功，TxHash: %s", submitResp.TxHash)
		// 6. 验证 token transfer 回执
		test.validateTokenTransferReceipt(t, transferReq, submitResp.TxHash)
	}
	t.Logf("✅ Erc20Transfer 集成测试通过")
}

func TestErc20Balance(t *testing.T) {
	test := NewVaultLaunchIntegrationTest(serverUrl, t)
	testCases := []struct {
		name      string
		sender    Signer
		TokenType client.RequestTokenType
	}{
		{
			name:      "查询usdc余额，用户1",
			sender:    users[0],
			TokenType: client.TokenTypeUSDC,
		},
		{
			name:      "查询vault token余额，用户1",
			sender:    users[0],
			TokenType: client.TokenTypeVaultToken,
		},
	}
	for _, tc := range testCases {
		t.Logf("开始执行测试用例: %s", tc.name)
		// 1. 准备 balance 请求
		var (
			tokenAddr string
		)
		switch tc.TokenType {
		case client.TokenTypeUSDC:
			tokenAddr = mockUSDC
		case client.TokenTypeVaultToken:
			// 先创建一个 vault
			vaultMsg := createVault(t, test, 24*time.Hour)
			// 给用户质押一些 vault token
			amount := parseUsd(100) // 100 USDC (6位精度)
			deposit(t, test, vaultMsg.VaultAddress, amount, tc.sender, tc.sender)
			tokenAddr = vaultMsg.VaultTokenAddress
		default:
			require.Fail(t, "不支持的 TokenType")
		}

		balanceReq := &client.RequestBalanceQueryReq{
			ChainId:   client.RequestChainId(chainId),
			UserAddr:  tc.sender.Address,
			TokenAddr: &tokenAddr,
			TokenType: tc.TokenType,
		}
		// 2. 调用 /api/v2/balance/get 接口
		balanceResp := test.callTokenBalance(t, balanceReq)
		require.NotNil(t, balanceResp)
		t.Logf("查询余额成功，用户: %s, 余额: %s", tc.sender.Address, balanceResp)
	}
}

// TestFundVaultRedeem FundVaultRedeem 集成测试
func TestFundVaultRedeem(t *testing.T) {
	// 创建测试实例，连接本地 token-engine 服务
	test := NewVaultLaunchIntegrationTest(serverUrl, t)

	testCases := []struct {
		name      string
		vaultType client.CommonVaultType
		sender    Signer
		receiver  Signer
		amount    string
	}{
		//{
		//	name:     "RBF模式下，单链支付，用户1质押给自己",
		//	sender:   users[0],
		//	receiver: users[0],
		//	amount:   parseUsd(1000), // 1000 USDC
		//},
		//{
		//	name:     "RBF模式下，多链支付,用户1(多链账户)质押给用户2",
		//	sender:   users[0],
		//	receiver: users[1],
		//	amount:   parseUsd(1000), // 1000 USDC
		//},
		{
			name:      "Fund模式下，单链支付，用户1质押给自己",
			sender:    users[0],
			receiver:  users[0],
			amount:    parseUsd(1005), // 1000 USDC
			vaultType: client.VaultTypeFund,
		},
		//{
		//	name:      "Fund模式下，多链支付,用户1(多链账户)质押给用户2",
		//	sender:    users[0],
		//	receiver:  users[1],
		//	amount:    parseUsd(1000), // 1000 USDC
		//	vaultType: client.VaultTypeFund,
		//},
	}

	for _, tc := range testCases {
		t.Logf("开始执行测试用例: %s", tc.name)
		// 1. 创建 Fund Vault
		var vaultMsg *client.VaultLaunch
		vaultMsg = createVault(t, test, 1*time.Minute, withVaultType(client.VaultTypeFund))
		vaultAddr := vaultMsg.VaultAddress
		deposit(t, test, vaultAddr, tc.amount, tc.sender, tc.receiver)
	}
}

func TestEventMatch(t *testing.T) {
	// 创建测试实例，连接本地 token-engine 服务
	test := NewVaultLaunchIntegrationTest(serverUrl, t)

	// 创建管理器实例
	balanceManager := NewBalanceManager(test, chainId, mockUSDC)
	transactionManager := NewTransactionManager(test, chainId)
	validationManager := NewValidationManager(test)

	t.Logf("开始 EventMatch 集成测试")
	testCases := []struct {
		name                string
		sender              Signer
		amount              string
		receiver            string
		aheadTx             bool
		changeNonce         bool
		insufficientBalance bool

		expectSuccessNum uint
	}{
		{
			name:             "用户1连续准备两笔交易，内容均为转账1USDC，不修改交易体，上链成功1笔，能够精确匹配，返回两个成功的结果",
			sender:           users[0],
			amount:           parseUsd(1), // 1 USDC
			receiver:         users[1].Address,
			changeNonce:      false,
			expectSuccessNum: 2,
		},
		{
			name:             "用户1连续发两笔交易,内容均为转账1USDC，修改交易体（nonce更改），上链成功，能够内容匹配，返回两个成功的结果",
			sender:           users[0],
			amount:           parseUsd(1),
			receiver:         users[1].Address,
			changeNonce:      true,
			expectSuccessNum: 2,
		},
		{
			name:             "用户1连续发两笔交易,内容均为转账1USDC，修改交易体（交易插队时的nonce更改），上链成功，能够内容匹配，返回两个成功的结果",
			sender:           users[0],
			amount:           parseUsd(1),
			receiver:         users[1].Address,
			changeNonce:      true,
			aheadTx:          true,
			expectSuccessNum: 3,
		},
		{
			name:                "用户1连续发两笔交易,内容均为转账1USDC，修改交易体（nonce更改），只有一笔交易上链成功，能够精确匹配，返回1个成功，1个失败的结果",
			sender:              users[0],
			amount:              parseUsd(1),
			receiver:            users[1].Address,
			changeNonce:         true,
			insufficientBalance: true,
			expectSuccessNum:    1,
		},
		{
			name:                "用户1连续发两笔交易,内容均为转账1USDC，修改交易体（交易插队后的两笔交易nonce更改），只有一笔交易上链成功，能够精确匹配，返回2个成功，1个失败的结果",
			sender:              users[0],
			amount:              parseUsd(1),
			receiver:            users[1].Address,
			changeNonce:         true,
			aheadTx:             true,
			insufficientBalance: true,
			expectSuccessNum:    2,
		},
	}

	for _, tc := range testCases {
		t.Logf("开始执行测试用例: %s", tc.name)

		// 1. 记录用户1的初始USDC余额（任何情况下都需要记录）
		initialBalance := balanceManager.RecordUserBalance(t, tc.sender.Address)

		// 2. 对于insufficientBalance的情况，需要确保余额满足条件：amount < 用户1余额 < 2*amount
		if tc.insufficientBalance {
			initialBalance = balanceManager.AdjustUserBalanceForTest(t, tc.sender.Address, admin, adminPrivateKey, tc.sender.PrivateKey, tc.amount)
		}

		// 3. 准备转账请求
		tokenAddr := mockUSDC
		transferReq1 := &client.RequestTransferPrepareReq{
			ChainId:   client.RequestChainId(chainId),
			FromAddr:  tc.sender.Address,
			ToAddr:    tc.receiver,
			TokenAddr: &tokenAddr,
			Amount:    tc.amount,
			TokenType: client.TokenTypeUSDC,
		}

		// 准备第二笔交易请求
		var transferReq2 *client.RequestTransferPrepareReq
		if tc.insufficientBalance {
			// 对于insufficientBalance的情况，第二笔交易使用相同金额
			// 由于我们已经确保用户1余额满足 amount < 余额 < 2*amount
			// 第一笔交易成功后，余额会减少amount，导致第二笔交易余额不足
			transferReq2 = &client.RequestTransferPrepareReq{
				ChainId:   client.RequestChainId(chainId),
				FromAddr:  tc.sender.Address,
				ToAddr:    tc.receiver,
				TokenAddr: &tokenAddr,
				Amount:    tc.amount, // 使用相同金额，第二笔会因余额不足而失败
				TokenType: client.TokenTypeUSDC,
			}
			t.Logf("第二笔交易使用相同金额: %s (预期因余额不足而失败)", transferReq2.Amount)
		} else {
			// 正常情况下，第二笔交易与第一笔相同
			transferReq2 = transferReq1
		}

		// 4. 准备并签名交易
		transactions := transactionManager.PrepareTransferTransactions(t, transferReq1, transferReq2, tc.changeNonce, tc.aheadTx, tc.sender.Address, tc.sender.PrivateKey)

		// 5. 预订阅交易 - 在提交交易前订阅，避免消息丢失
		expectedTxHashes := make([]string, 0)
		for _, tx := range transactions {
			if tx.Type == TokenTransfer {
				expectedTxHashes = append(expectedTxHashes, tx.TxHash)
			}
		}
		subscriptions := transactionManager.PreSubscribeMQ(t, expectedTxHashes, client.MessageTypeTokenTransfer)
		defer transactionManager.CleanupSubscriptions(subscriptions)

		// 6. 提交已签名交易
		submitResults := transactionManager.SubmitTransactions(t, transactions, tc.sender.Address, tc.sender.PrivateKey)

		// 等待预订阅的MQ消息
		mqMessages := transactionManager.WaitForMQMessages(t, subscriptions, 30)

		// 7. 验证交易结果（使用预订阅的消息）
		validationResult := validationManager.ValidateTransactionResultsWithMQMessages(t, submitResults, mqMessages)

		// 8. 验证测试用例
		validationManager.ValidateTestCase(t, tc.name, validationResult, tc.expectSuccessNum, tc.changeNonce, tc.aheadTx)

		// 9. 恢复sender的USDC余额
		balanceManager.RestoreUserBalance(t, tc.sender.Address, admin, adminPrivateKey, initialBalance)
	}

	t.Logf("✅ EventMatch 集成测试通过")
}

// vaultLaunchOption 定义vault
type vaultLaunchOption func(*vaultLaunchOptions)

// vaultLaunchOptions 定义vault配置选项
type vaultLaunchOptions struct {
	vaultType client.CommonVaultType
}

// withVaultType 设置vault类型
func withVaultType(vaultType client.CommonVaultType) vaultLaunchOption {
	return func(opts *vaultLaunchOptions) {
		opts.vaultType = vaultType
	}
}

func createVault(t *testing.T, test *VaultLaunchIntegrationTest, fundingDuration time.Duration, options ...vaultLaunchOption) *client.VaultLaunch {
	t.Logf("开始 VaultLaunch 集成测试")

	// 1. 准备 VaultLaunch 请求
	deployerAddress := admin

	// 创建请求
	vaultCreateReq := &client.RequestVaultCreateReq{
		ChainId: client.CommonChainID(chainId),
		ManagementData: client.RequestVaultManagement{
			Deployer:        deployerAddress,
			Issuer:          deployerAddress,
			Manager:         deployerAddress,
			Withdrawer:      deployerAddress,
			DividendManager: deployerAddress,
		},
		TokenMetaData: client.RequestTokenMeta{
			TokenName:     "Test Vault Token",
			TokenSymbol:   "TVT",
			TokenDecimals: int(config.Blockchain.Decimal),
			TokenUri:      "https://example.com/token/1",
		},
		FinancingRuleData: client.RequestFinancingRuleInfo{
			ProjectName:                        fmt.Sprintf("test_%d", time.Now().Unix()),
			FinancingCurrencyAddr:              mockUSDC,       // mockUSDC
			TokenMaxSupply:                     parseUsd(1000), // 1000 vlt
			FinancingStartTime:                 intPtr(int(time.Now().Unix())),
			FinancingDeadline:                  intPtr(int(time.Now().Add(fundingDuration).Unix())),
			MinInvestmentBaseFinancingCurrency: parseUsd(10),  // 10 USDC
			SoftCap:                            parseUsd(750), // 750 vlt
			// 将字符串常量转换为指针
			ExcessFundraisingRatioBps: stringPtr("500"),       // 5%
			SharePrice:                stringPtr(parseUsd(1)), // 1 U
			ManageFeeBps:              stringPtr("50"),        // 0.5%
			FundingReceiver:           stringPtr(deployerAddress),
			ManageFeeReceiver:         stringPtr(deployerAddress),
			DecimalsMultiplier:        stringPtr("1"),
			EnableWhitelist:           boolPtr(false),
			Whitelist:                 &[]string{},
		},
	}

	opts := &vaultLaunchOptions{}
	for _, option := range options {
		option(opts)
	}
	vaultType := opts.vaultType
	vaultCreateReq.VaultType = &vaultType
	if vaultType == client.VaultTypeFund {
		minAmount := parseUsd(10)
		vaultCreateReq.FundExtraData = &client.RequestFundExtraData{
			StartTime:           intPtr(int(time.Now().Add(fundingDuration).Unix())),
			MinRedemptionAmount: &minAmount,
		}
	}

	// 2. 调用 /api/v2/primary/vault/prepare_create 接口
	prepareResp := test.callPrepareCreateVault(t, vaultCreateReq)
	require.NotNil(t, prepareResp)
	require.NotEmpty(t, prepareResp.TxMsgBase64)

	t.Logf("准备交易成功，CorrelationId: %s", prepareResp.CorrelationId)
	tx := &types.Transaction{}
	data, err := base64.StdEncoding.DecodeString(prepareResp.TxMsgBase64)
	require.NoError(t, err)
	err = tx.UnmarshalBinary(data)
	require.NoError(t, err)
	// 3. 签名交易
	signedTx, err := signTransaction(t, string(vaultCreateReq.ChainId), adminPrivateKey, tx)
	require.NoError(t, err)

	// 编码为base64
	signedTxBytes, err := signedTx.MarshalBinary()
	require.NoError(t, err)
	signedTxBase64 := base64.StdEncoding.EncodeToString(signedTxBytes)

	t.Logf("交易签名成功，准备提交交易")
	// 准备一个 channel 用于接收 MQ 消息
	mqMessageInterfaceCh := make(chan interface{}, 1)
	go func() {
		res := test.waitForMQMessage(t, signedTx.Hash().String(), client.MessageTypeVaultLaunch)
		mqMessageInterfaceCh <- res
	}()
	// 4. 调用 /api/v1/common/submit_tx 接口提交交易
	submitResp := test.callSubmitTx(t, &client.RequestSubmitReq{
		ChainId:      vaultCreateReq.ChainId,
		Sender:       vaultCreateReq.ManagementData.Deployer,
		TxMsgBase64:  prepareResp.TxMsgBase64,
		SignTxBase64: signedTxBase64,
	})
	require.NotNil(t, submitResp)
	require.NotEmpty(t, submitResp.TxHash)

	t.Logf("交易提交成功，TxHash: %s", submitResp.TxHash)

	// 5. 等待 MQ 推送
	mqMessage := &client.VaultLaunch{}
	var ok bool
	select {
	case mqMessageInterface := <-mqMessageInterfaceCh:
		// 类型断言
		mqMessage, ok = mqMessageInterface.(*client.VaultLaunch)
		require.True(t, ok, "MQ 消息类型断言失败")
	case <-time.After(70 * time.Second):
		require.Fail(t, "等待 MQ 消息超时")
	}

	// 6. 验证 MQ 消息内容
	test.validateVaultLaunchMQMessage(t, mqMessage, submitResp.TxHash)

	t.Logf("✅ VaultLaunch 集成测试通过")
	t.Logf("   Vault 地址: %s", mqMessage.VaultAddress)
	t.Logf("   Vault Token 地址: %s", mqMessage.VaultTokenAddress)
	t.Logf("   交易哈希: %s", submitResp.TxHash)
	return mqMessage
}

func redeem(t *testing.T, test *VaultLaunchIntegrationTest, vaultAddress string, signer Signer, amount string) string {
	t.Logf("开始执行 redeem 测试，Vault 地址: %s", vaultAddress)
	// 1. 准备 redeem 请求 - 先进行 approve vault token
	approveReq := &VaultApproveRedeemRequest{
		ChainId:      chainId,
		Investor:     signer.Address,
		VaultAddress: vaultAddress,
		Amount:       amount,
	}

	// 1.1 调用 /api/v2/primary/vault/prepare_redeem_approve 接口
	approveResp := test.callPrepareRedeemApprove(t, approveReq)
	require.NotNil(t, approveResp)
	require.NotEmpty(t, approveResp.TxMsgBase64)
	time.Sleep(2 * time.Second)

	// 1.2 签名 approve 交易
	approveTx := &types.Transaction{}
	approveData, err := base64.StdEncoding.DecodeString(approveResp.TxMsgBase64)
	require.NoError(t, err)
	err = approveTx.UnmarshalBinary(approveData)
	require.NoError(t, err)

	signedApproveTx, err := signTransaction(t, approveReq.ChainId, signer.PrivateKey, approveTx)
	require.NoError(t, err)

	// 1.3 提交 approve 交易
	approveSubmitResp := test.callSubmitTx(t, &client.RequestSubmitReq{
		ChainId:      client.CommonChainID(approveReq.ChainId),
		Sender:       approveReq.Investor,
		TxMsgBase64:  approveResp.TxMsgBase64,
		SignTxBase64: encodeTransactionToBase64(t, signedApproveTx),
	})
	require.NotNil(t, approveSubmitResp)
	require.NotEmpty(t, approveSubmitResp.TxHash)

	t.Logf("Approve 交易已提交，交易哈希: %s", approveSubmitResp.TxHash)

	// 2. 调用 /api/v2/primary/vault/pre_prepare_redeem 接口获取管理员签名数据
	prePrepareReq := &client.RequestVaultRedeemReq{
		ChainId:       client.CommonChainID(chainId),
		AssetReceiver: signer.Address,
		Investor:      signer.Address,
		VaultAddress:  vaultAddress,
		Amount:        amount,
	}

	prePrepareResp := test.callPrePrepareRedeem(t, prePrepareReq)
	require.NotNil(t, prePrepareResp)
	require.NotEmpty(t, prePrepareResp.DataBase64)

	// 2.1 Parse private key
	privateKey, err := crypto.HexToECDSA(adminPrivateKey)
	require.NoError(t, err)
	// 3. 管理员签名数据
	adminSignature, err := generateAdminSign(prePrepareResp.DataBase64, privateKey)
	require.NoError(t, err)

	// 签名结果进行 base64 编码
	signstr := base64.StdEncoding.EncodeToString(adminSignature)

	// 4. 准备 redeem 请求（包含管理员签名）
	redeemReq := &client.RequestVaultRedeemReq{
		ChainId:       client.CommonChainID(chainId),
		Investor:      signer.Address,
		AssetReceiver: signer.Address,
		VaultAddress:  vaultAddress,
		Amount:        amount,
		Signature:     &signstr,
	}

	// 5. 调用 /api/v2/primary/vault/prepare_redeem 接口
	redeemResp := test.callPrepareRedeem(t, redeemReq)
	require.NotNil(t, redeemResp)
	require.NotEmpty(t, redeemResp.TxMsgBase64)

	// 6. 签名 redeem 交易
	redeemTx := &types.Transaction{}
	redeemData, err := base64.StdEncoding.DecodeString(redeemResp.TxMsgBase64)
	require.NoError(t, err)
	err = redeemTx.UnmarshalBinary(redeemData)
	require.NoError(t, err)

	signedRedeemTx, err := signTransaction(t, string(redeemReq.ChainId), signer.PrivateKey, redeemTx)
	require.NoError(t, err)

	// 准备一个 channel 用于接收 MQ 消息
	mqMessageInterfaceCh := make(chan interface{}, 1)
	go func() {
		res := test.waitForMQMessage(t, signedRedeemTx.Hash().String(), client.MessageTypeVaultRedeem)
		mqMessageInterfaceCh <- res
	}()

	// 7. 调用 /api/v1/common/submit_tx 接口提交 redeem 交易
	redeemSubmitResp := test.callSubmitTx(t, &client.RequestSubmitReq{
		ChainId:      redeemReq.ChainId,
		Sender:       redeemReq.Investor,
		TxMsgBase64:  redeemResp.TxMsgBase64,
		SignTxBase64: encodeTransactionToBase64(t, signedRedeemTx),
	})
	require.NotNil(t, redeemSubmitResp)
	require.NotEmpty(t, redeemSubmitResp.TxHash)

	t.Logf("Redeem 交易已提交，交易哈希: %s", redeemSubmitResp.TxHash)

	// 8. 等待 MQ 推送
	mqMessage := &client.VaultRedeem{}
	var ok bool
	select {
	case mqMessageInterface := <-mqMessageInterfaceCh:
		mqMessage, ok = mqMessageInterface.(*client.VaultRedeem)
		require.True(t, ok, "MQ 消息类型断言失败")
	case <-time.After(70 * time.Second):
		require.Fail(t, "等待 MQ 消息超时")
	}

	// 9. 验证 MQ 消息内容
	test.validateVaultRedeemMQMessage(t, mqMessage, redeemReq, redeemSubmitResp.TxHash)

	t.Logf("✅ Redeem 集成测试通过")
	t.Logf("   赎回金额: %s USDC", mqMessage.AssetTokenAmount)
	t.Logf("   交易哈希: %s", redeemSubmitResp.TxHash)
	return mqMessage.AssetTokenAmount
}

func deposit(t *testing.T, test *VaultLaunchIntegrationTest, vaultAddress, amount string, sender, receiver Signer) *client.VaultInvest {
	t.Logf("开始执行 deposit 测试，Vault 地址: %s", vaultAddress)
	t.Logf("开始approve usdt, user: %s", sender)
	// 1. 准备 VaultInvest 请求 - 先进行 approve
	approveReq := &VaultDepositRequest{
		ChainId:      chainId,
		Sender:       sender.Address,
		Investor:     receiver.Address,
		VaultAddress: vaultAddress,
		Amount:       amount,
	}

	// 1.1 调用 /api/v2/primary/vault/prepare_deposit_approve 接口
	approveResp := test.callPrepareDepositApprove(t, approveReq)
	require.NotNil(t, approveResp)
	require.NotEmpty(t, approveResp.TxMsgBase64)

	// 1.2 签名 approve 交易
	approveTx := &types.Transaction{}
	approveData, err := base64.StdEncoding.DecodeString(approveResp.TxMsgBase64)
	require.NoError(t, err)
	err = approveTx.UnmarshalBinary(approveData)
	require.NoError(t, err)

	signedApproveTx, err := signTransaction(t, approveReq.ChainId, sender.PrivateKey, approveTx)
	require.NoError(t, err)

	// 1.3 提交 approve 交易
	approveSubmitResp := test.callSubmitTx(t, &client.RequestSubmitReq{
		ChainId:      client.CommonChainID(approveReq.ChainId),
		Sender:       approveReq.Sender,
		TxMsgBase64:  approveResp.TxMsgBase64,
		SignTxBase64: encodeTransactionToBase64(t, signedApproveTx),
	})
	require.NotNil(t, approveSubmitResp)
	require.NotEmpty(t, approveSubmitResp.TxHash)

	t.Logf("Approve 交易已提交，交易哈希: %s", approveSubmitResp.TxHash)

	// 2. 调用 /api/v2/primary/vault/pre_prepare_deposit 接口获取管理员签名数据
	prePrepareResp := test.callPrePrepareDeposit(t, approveReq)
	require.NotNil(t, prePrepareResp)
	require.NotEmpty(t, prePrepareResp.DataBase64)

	// 3. 管理员签名数据
	// Parse private key
	privateKey, err := crypto.HexToECDSA(adminPrivateKey)
	require.NoError(t, err)

	adminSignature, err := generateAdminSign(prePrepareResp.DataBase64, privateKey)
	require.NoError(t, err)

	signstr := base64.StdEncoding.EncodeToString(adminSignature)

	// 4. 准备 deposit 请求（包含管理员签名）
	depositReq := &VaultDepositRequest{
		ChainId:      chainId,
		Sender:       sender.Address,
		Investor:     receiver.Address,
		VaultAddress: vaultAddress,
		Amount:       amount,
		Signature:    signstr,
	}

	// 5. 调用 /api/v2/primary/vault/prepare_deposit 接口
	depositResp := test.callPrepareDeposit(t, depositReq)
	require.NotNil(t, depositResp)
	require.NotEmpty(t, depositResp.TxMsgBase64)

	// 6. 管理员签名 deposit 交易
	depositTx := &types.Transaction{}
	depositData, err := base64.StdEncoding.DecodeString(depositResp.TxMsgBase64)
	require.NoError(t, err)
	err = depositTx.UnmarshalBinary(depositData)
	require.NoError(t, err)

	// user签名
	signedDepositTx, err := signTransaction(t, depositReq.ChainId, sender.PrivateKey, depositTx)
	require.NoError(t, err)

	// 准备一个 channel 用于接收 MQ 消息
	mqMessageInterfaceCh := make(chan interface{}, 1)
	go func() {
		res := test.waitForMQMessage(t, signedDepositTx.Hash().String(), client.MessageTypeVaultInvest)
		mqMessageInterfaceCh <- res
	}()

	// 7. 调用 /api/v1/common/submit_tx 接口提交 deposit 交易
	depositSubmitResp := test.callSubmitTx(t, &client.RequestSubmitReq{
		ChainId:      client.CommonChainID(depositReq.ChainId),
		Sender:       depositReq.Sender,
		TxMsgBase64:  depositResp.TxMsgBase64,
		SignTxBase64: encodeTransactionToBase64(t, signedDepositTx),
	})
	require.NotNil(t, depositSubmitResp)
	require.NotEmpty(t, depositSubmitResp.TxHash)

	t.Logf("Deposit 交易已提交，交易哈希: %s", depositSubmitResp.TxHash)

	// 8. 等待 MQ 推送
	mqMessage := &client.VaultInvest{}
	var ok bool
	select {
	case mqMessageInterface := <-mqMessageInterfaceCh:
		// 类型断言
		mqMessage, ok = mqMessageInterface.(*client.VaultInvest)
		require.True(t, ok, "MQ 消息类型断言失败")
	case <-time.After(70 * time.Second):
		require.Fail(t, "等待 MQ 消息超时")
	}

	// 9. 验证 MQ 消息内容
	test.validateVaultInvestMQMessage(t, mqMessage, depositReq, depositSubmitResp.TxHash)

	t.Logf("✅ Deposit 集成测试通过")
	t.Logf("   投资金额: %s USDC", depositReq.Amount)
	t.Logf("   获得 Vault Token 数量: %s", mqMessage.VaultTokenAmount)
	t.Logf("   交易哈希: %s", depositSubmitResp.TxHash)
	return mqMessage
}

func offchainDeposit(t *testing.T, test *VaultLaunchIntegrationTest, vaultAddress string, amount string, receiver Signer) {
	t.Logf("开始执行 offchain deposit 测试，Vault 地址: %s", vaultAddress)
	// 1. 准备 offchain deposit 请求
	depositReq := &client.RequestOffChainDepositReq{
		ChainId:      client.CommonChainID(chainId),
		Manager:      admin,
		Recipient:    receiver.Address,
		VaultAddress: vaultAddress,
		Amount:       amount,
	}

	// 2. 调用 /api/v2/primary/vault/prepare_off_chain_deposit 接口
	depositResp := test.callPrepareOffchainDeposit(t, depositReq)
	require.NotNil(t, depositResp)
	require.NotEmpty(t, depositResp.TxMsgBase64)

	// 3. 签名 offchain deposit 交易
	depositTx := &types.Transaction{}
	depositData, err := base64.StdEncoding.DecodeString(depositResp.TxMsgBase64)
	require.NoError(t, err)
	err = depositTx.UnmarshalBinary(depositData)
	require.NoError(t, err)

	signedDepositTx, err := signTransaction(t, string(depositReq.ChainId), adminPrivateKey, depositTx)
	require.NoError(t, err)

	// 准备一个 channel 用于接收 MQ 消息
	mqMessageInterfaceCh := make(chan interface{}, 1)
	go func() {
		res := test.waitForMQMessage(t, signedDepositTx.Hash().String(), client.MessageTypeOffChainDeposit)
		mqMessageInterfaceCh <- res
	}()

	// 4. 调用 /api/v1/common/submit_tx 接口提交 offchain deposit 交易
	depositSubmitResp := test.callSubmitTx(t, &client.RequestSubmitReq{
		ChainId:      depositReq.ChainId,
		Sender:       depositReq.Manager,
		TxMsgBase64:  depositResp.TxMsgBase64,
		SignTxBase64: encodeTransactionToBase64(t, signedDepositTx),
	})
	require.NotNil(t, depositSubmitResp)
	require.NotEmpty(t, depositSubmitResp.TxHash)

	t.Logf("Offchain Deposit 交易已提交，交易哈希: %s", depositSubmitResp.TxHash)

	// 5. 等待 MQ 推送
	mqMessage := &client.OffChainDeposit{}
	var ok bool
	select {
	case mqMessageInterface := <-mqMessageInterfaceCh:
		// 类型断言
		mqMessage, ok = mqMessageInterface.(*client.OffChainDeposit)
		require.True(t, ok, "MQ 消息类型断言失败")
	case <-time.After(70 * time.Second):
		require.Fail(t, "等待 MQ 消息超时")
	}

	// 6. 验证 MQ 消息内容
	test.validateVaultOffChainInvestMQMessage(t, mqMessage, depositReq, depositSubmitResp.TxHash)
	t.Logf("✅ Offchain Deposit 集成测试通过")
}

func dividend(t *testing.T, test *VaultLaunchIntegrationTest, vaultAddress string, amount string, drdsNonce int) {
	t.Logf("开始执行 dividend 测试，Vault 地址: %s", vaultAddress)
	// 0. 准备 approve VaultDividend 请求, 调用 /api/v2/primary/vault/prepare_dividend_approve 接口
	t.Logf("开始approve usdt, user: %s", admin)
	// 1. 准备 VaultInvest 请求 - 先进行 approve
	approveReq := &VaultApproveDividendRequest{
		ChainId:      chainId,
		Manager:      admin,
		VaultAddress: vaultAddress,
		Amount:       amount,
	}

	// 1.1 调用 /api/v2/primary/vault/prepare_deposit_approve 接口
	approveResp := test.callPrepareDividendApprove(t, approveReq)
	require.NotNil(t, approveResp)
	require.NotEmpty(t, approveResp.TxMsgBase64)

	// 1.2 签名 approve 交易
	approveTx := &types.Transaction{}
	approveData, err := base64.StdEncoding.DecodeString(approveResp.TxMsgBase64)
	require.NoError(t, err)
	err = approveTx.UnmarshalBinary(approveData)
	require.NoError(t, err)

	signedApproveTx, err := signTransaction(t, approveReq.ChainId, adminPrivateKey, approveTx)
	require.NoError(t, err)

	// 1.3 提交 approve 交易
	approveSubmitResp := test.callSubmitTx(t, &client.RequestSubmitReq{
		ChainId:      client.CommonChainID(approveReq.ChainId),
		Sender:       approveReq.Manager,
		TxMsgBase64:  approveResp.TxMsgBase64,
		SignTxBase64: encodeTransactionToBase64(t, signedApproveTx),
	})
	require.NotNil(t, approveSubmitResp)
	require.NotEmpty(t, approveSubmitResp.TxHash)

	t.Logf("Approve 交易已提交，交易哈希: %s", approveSubmitResp.TxHash)

	// 1. 准备 VaultDividend 请求，其中签名字段, 使用管理员私钥签名, 使用generateDrdsDividendSign方法
	// 解析管理员私钥
	privateKey, err := crypto.HexToECDSA(adminPrivateKey)
	require.NoError(t, err)

	// 生成分红签名
	// 这里需要 nonce，暂时使用 全局的nonce，实际应该从链上获取
	nonce := big.NewInt(int64(drdsNonce))
	amountBigInt, ok := new(big.Int).SetString(amount, 10)
	require.True(t, ok, "无效的分红金额")

	signature, err := generateDrdsDividendSign(vaultAddress, nonce, amountBigInt, privateKey)
	require.NoError(t, err)

	// 签名结果hex编码
	hexSign := hexutil.Encode(signature)

	// 准备分红请求
	dividendReq := &client.RequestVaultDistributeDividendReq{
		ChainId:   client.CommonChainID(chainId),
		UserAddr:  &admin, // 管理员地址
		Signature: hexSign,
		Amount:    amount,
		AssetAddr: &mockUSDC, // mockUSDC
		VaultAddr: &vaultAddress,
	}

	// 2. 调用 /api/v2/primary/vault/prepare_distribute_dividend 接口
	prepareResp := test.callPrepareDividend(t, dividendReq)
	require.NotNil(t, prepareResp)
	require.NotEmpty(t, prepareResp.TxMsgBase64)

	t.Logf("准备分红交易成功，CorrelationId: %s", prepareResp.CorrelationId)

	// 3. 调用 /api/v1/common/submit_tx 接口提交 dividend 交易
	// 解析交易数据
	tx := &types.Transaction{}
	data, err := base64.StdEncoding.DecodeString(prepareResp.TxMsgBase64)
	require.NoError(t, err)
	err = tx.UnmarshalBinary(data)
	require.NoError(t, err)

	// 签名交易
	signedTx, err := signTransaction(t, string(dividendReq.ChainId), adminPrivateKey, tx)
	require.NoError(t, err)

	// 准备一个 channel 用于接收 MQ 消息
	mqMessageInterfaceCh := make(chan interface{}, 1)
	go func() {
		res := test.waitForMQMessage(t, signedTx.Hash().String(), client.MessageTypeVaultDividend)
		mqMessageInterfaceCh <- res
	}()

	// 提交交易
	submitResp := test.callSubmitTx(t, &client.RequestSubmitReq{
		ChainId:      dividendReq.ChainId,
		Sender:       *dividendReq.UserAddr,
		TxMsgBase64:  prepareResp.TxMsgBase64,
		SignTxBase64: encodeTransactionToBase64(t, signedTx),
	})
	require.NotNil(t, submitResp)
	require.NotEmpty(t, submitResp.TxHash)

	t.Logf("分红交易提交成功，TxHash: %s", submitResp.TxHash)

	// 4. 等待 MQ 推送
	var mqMessage *client.VaultDividend
	select {
	case mqMessageInterface := <-mqMessageInterfaceCh:
		// 类型断言
		mqMessage, ok = mqMessageInterface.(*client.VaultDividend)
		require.True(t, ok, "MQ 消息类型断言失败")
	case <-time.After(70 * time.Second):
		require.Fail(t, "等待 MQ 消息超时")
	}

	// 5. 验证 MQ 消息内容
	test.validateVaultDividendMQMessage(t, mqMessage, dividendReq, submitResp.TxHash)

	t.Logf("✅ Dividend 集成测试通过")
	t.Logf("   分红金额: %s USDC", mqMessage.AssetTokenAmount)
	t.Logf("   交易哈希: %s", submitResp.TxHash)
}

func claim(t *testing.T, test *VaultLaunchIntegrationTest, vaultAddress string, user Signer) {
	t.Logf("开始执行 claim 测试，Vault 地址: %s", vaultAddress)
	// 1. 准备 VaultClaim 请求, 调用 /api/v2/primary/vault/prepare_claim_reward 接口
	claimReq := &client.RequestVaultClaimRewardReq{
		ChainId:      client.CommonChainID(chainId),
		Investor:     user.Address,
		AssetAddress: &mockUSDC,
		VaultAddress: vaultAddress,
		Amount:       "5000000", // 随便填一个数，claim 接口会忽略这个字段
	}
	claimResp := test.callPrepareClaim(t, claimReq)
	require.NotNil(t, claimResp)
	require.NotEmpty(t, claimResp.TxMsgBase64)
	// 2. 发送交易
	tx := &types.Transaction{}
	data, err := base64.StdEncoding.DecodeString(claimResp.TxMsgBase64)
	require.NoError(t, err)
	err = tx.UnmarshalBinary(data)
	require.NoError(t, err)
	signedTx, err := signTransaction(t, string(claimReq.ChainId), user.PrivateKey, tx)
	require.NoError(t, err)

	// 准备一个 channel 用于接收 MQ 消息
	mqMessageInterfaceCh := make(chan interface{}, 1)
	go func() {
		res := test.waitForMQMessage(t, signedTx.Hash().String(), client.MessageTypeVaultClaim)
		mqMessageInterfaceCh <- res
	}()

	submitResp := test.callSubmitTx(t, &client.RequestSubmitReq{
		ChainId:      claimReq.ChainId,
		Sender:       claimReq.Investor,
		TxMsgBase64:  claimResp.TxMsgBase64,
		SignTxBase64: encodeTransactionToBase64(t, signedTx),
	})
	require.NotNil(t, submitResp)
	require.NotEmpty(t, submitResp.TxHash)
	t.Logf("Claim 交易提交成功，TxHash: %s", submitResp.TxHash)
	// 3. 等待 MQ 推送
	var mqMessage *client.VaultClaim
	var ok bool
	select {
	case mqMessageInterface := <-mqMessageInterfaceCh:
		// 类型断言
		mqMessage, ok = mqMessageInterface.(*client.VaultClaim)
		require.True(t, ok, "MQ 消息类型断言失败")
	case <-time.After(70 * time.Second):
		require.Fail(t, "等待 MQ 消息超时")
	}
	// 4. 验证 MQ 消息内容
	test.validateVaultClaimMQMessage(t, mqMessage, claimReq, submitResp.TxHash)
	t.Logf("  用户 %s 成功领取金额: %s USDC", mqMessage.Sender, mqMessage.AssetTokenAmount)
	t.Logf("✅ Claim 集成测试通过")
}

func unPauseToken(t *testing.T, test *VaultLaunchIntegrationTest, vaultAddress string) {
	t.Logf("开始执行 unPauseToken 测试")
	// 1. 准备 unPauseToken 请求
	unPauseReq := &client.RequestVaultUnPauseTokenReq{
		ChainId:      client.CommonChainID(chainId),
		Manager:      &admin,
		VaultAddress: vaultAddress,
	}
	// 2. 调用 /api/v2/primary/token/prepare_unpause_token 接口
	unPauseResp := test.callPrepareUnPauseToken(t, unPauseReq)
	require.NotNil(t, unPauseResp)
	require.NotEmpty(t, unPauseResp.TxMsgBase64)
	t.Logf("准备交易成功，CorrelationId: %s", unPauseResp.CorrelationId)
	tx := &types.Transaction{}
	data, err := base64.StdEncoding.DecodeString(unPauseResp.TxMsgBase64)
	require.NoError(t, err)
	err = tx.UnmarshalBinary(data)
	require.NoError(t, err)
	// 3. 签名交易
	signedTx, err := signTransaction(t, string(unPauseReq.ChainId), adminPrivateKey, tx)
	require.NoError(t, err)
	t.Logf("交易签名成功，准备提交交易")
	// 4. 调用 /api/v1/common/submit_tx 接口提交交易
	submitResp := test.callSubmitTx(t, &client.RequestSubmitReq{
		ChainId:      unPauseReq.ChainId,
		Sender:       *unPauseReq.Manager,
		TxMsgBase64:  unPauseResp.TxMsgBase64,
		SignTxBase64: encodeTransactionToBase64(t, signedTx),
	})
	require.NotNil(t, submitResp)
	require.NotEmpty(t, submitResp.TxHash)
	t.Logf("交易提交成功，TxHash: %s", submitResp.TxHash)
	// 5. 等待并验证 MQ 消息内容
	test.validateVaultUnPauseTokenReceipt(t, submitResp.TxHash)
	t.Logf("✅ UnPauseToken 集成测试通过")
}

func withdrawManageFee(t *testing.T, test *VaultLaunchIntegrationTest, vaultAddress string) {
	t.Logf("开始执行 withdrawManageFee 测试")
	// 1. 准备 withdrawManageFee 请求
	withdrawReq := &client.RequestVaultWithdrawManagerFeeReq{
		ChainId:      client.CommonChainID(chainId),
		Withdrawer:   admin,
		VaultAddress: vaultAddress,
	}
	// 2. 调用 /api/v2/primary/vault/prepare_withdraw_fee 接口
	withdrawResp := test.callPrepareWithdrawManageFee(t, withdrawReq)
	require.NotNil(t, withdrawResp)
	require.NotEmpty(t, withdrawResp.TxMsgBase64)
	t.Logf("准备交易成功，CorrelationId: %s", withdrawResp.CorrelationId)
	tx := &types.Transaction{}
	data, err := base64.StdEncoding.DecodeString(withdrawResp.TxMsgBase64)
	require.NoError(t, err)
	err = tx.UnmarshalBinary(data)
	require.NoError(t, err)
	// 3. 签名交易
	signedTx, err := signTransaction(t, string(withdrawReq.ChainId), adminPrivateKey, tx)
	require.NoError(t, err)
	t.Logf("交易签名成功，准备提交交易")

	// 准备一个 channel 用于接收 MQ 消息
	mqMessageInterfaceCh := make(chan interface{}, 1)
	go func() {
		res := test.waitForMQMessage(t, signedTx.Hash().String(), client.MessageTypeWithdrawManageFee)
		mqMessageInterfaceCh <- res
	}()

	// 4. 调用 /api/v1/common/submit_tx 接口提交交易
	submitResp := test.callSubmitTx(t, &client.RequestSubmitReq{
		ChainId:      withdrawReq.ChainId,
		Sender:       withdrawReq.Withdrawer,
		TxMsgBase64:  withdrawResp.TxMsgBase64,
		SignTxBase64: encodeTransactionToBase64(t, signedTx),
	})
	require.NotNil(t, submitResp)
	require.NotEmpty(t, submitResp.TxHash)
	t.Logf("交易提交成功，TxHash: %s", submitResp.TxHash)

	// 5. 等待并验证 MQ 消息内容
	var (
		mqMessage *client.VaultWithdrawFee
		ok        bool
	)

	select {
	case mqMessageInterface := <-mqMessageInterfaceCh:
		// 类型断言
		mqMessage, ok = mqMessageInterface.(*client.VaultWithdrawFee)
		require.True(t, ok, "MQ 消息类型断言失败")
	case <-time.After(70 * time.Second):
		require.Fail(t, "等待 MQ 消息超时")
	}
	test.validateVaultWithdrawManageFeeMQMessage(t, mqMessage, withdrawReq, submitResp.TxHash)
	t.Logf("✅ WithdrawManageFee 集成测试通过")
}

func withdraw(t *testing.T, test *VaultLaunchIntegrationTest, vaultAddress string) {
	t.Logf("开始执行 withdraw 测试")
	// 1. 准备 withdraw 请求
	withdrawReq := &client.RequestVaultWithdrawAssetReq{
		ChainId:      client.CommonChainID(chainId),
		Withdrawer:   admin,
		VaultAddress: vaultAddress,
	}
	// 2. 调用 /api/v2/primary/vault/prepare_withdraw 接口
	withdrawResp := test.callPrepareWithdraw(t, withdrawReq)
	require.NotNil(t, withdrawResp)
	require.NotEmpty(t, withdrawResp.TxMsgBase64)
	t.Logf("准备交易成功，CorrelationId: %s", withdrawResp.CorrelationId)
	tx := &types.Transaction{}
	data, err := base64.StdEncoding.DecodeString(withdrawResp.TxMsgBase64)
	require.NoError(t, err)
	err = tx.UnmarshalBinary(data)
	require.NoError(t, err)
	// 3. 签名交易
	signedTx, err := signTransaction(t, string(withdrawReq.ChainId), adminPrivateKey, tx)
	require.NoError(t, err)
	t.Logf("交易签名成功，准备提交交易")

	// 准备一个 channel 用于接收 MQ 消息
	mqMessageInterfaceCh := make(chan interface{}, 1)
	go func() {
		res := test.waitForMQMessage(t, signedTx.Hash().String(), client.MessageTypeVaultWithdraw)
		mqMessageInterfaceCh <- res
	}()
	// 4. 调用 /api/v1/common/submit_tx 接口提交交易
	submitResp := test.callSubmitTx(t, &client.RequestSubmitReq{
		ChainId:      withdrawReq.ChainId,
		Sender:       withdrawReq.Withdrawer,
		TxMsgBase64:  withdrawResp.TxMsgBase64,
		SignTxBase64: encodeTransactionToBase64(t, signedTx),
	})
	require.NotNil(t, submitResp)
	require.NotEmpty(t, submitResp.TxHash)
	t.Logf("交易提交成功，TxHash: %s", submitResp.TxHash)

	// 5. 等待并验证 MQ 消息内容
	var (
		mqMessage *client.VaultWithdraw
		ok        bool
	)
	select {
	case mqMessageInterface := <-mqMessageInterfaceCh:
		// 类型断言
		mqMessage, ok = mqMessageInterface.(*client.VaultWithdraw)
		require.True(t, ok, "MQ 消息类型断言失败")
	case <-time.After(70 * time.Second):
		require.Fail(t, "等待 MQ 消息超时")
	}
	test.validateVaultWithdrawMQMessage(t, mqMessage, withdrawReq, submitResp.TxHash)
	t.Logf("✅ Withdraw 集成测试通过")
}

func fundVaultRedemptionRequest(t *testing.T, test *VaultLaunchIntegrationTest, vaultAddress, amount string, sender, receiver Signer) *client.VaultInvest {
	t.Logf("开始执行 fundVaultRedemptionRequest 测试，Vault 地址: %s", vaultAddress)
	t.Logf("开始approve vault token, user: %s", sender)
	// 1. 准备 VaultInvest 请求 - 先进行 approve
	approveReq := &VaultDepositRequest{
		ChainId:      chainId,
		Sender:       sender.Address,
		Investor:     receiver.Address,
		VaultAddress: vaultAddress,
		Amount:       amount,
	}

	// 1.1 调用 /api/v2/primary/vault/prepare_deposit_approve 接口
	approveResp := test.callPrepareDepositApprove(t, approveReq)
	require.NotNil(t, approveResp)
	require.NotEmpty(t, approveResp.TxMsgBase64)

	// 1.2 签名 approve 交易
	approveTx := &types.Transaction{}
	approveData, err := base64.StdEncoding.DecodeString(approveResp.TxMsgBase64)
	require.NoError(t, err)
	err = approveTx.UnmarshalBinary(approveData)
	require.NoError(t, err)

	signedApproveTx, err := signTransaction(t, approveReq.ChainId, sender.PrivateKey, approveTx)
	require.NoError(t, err)

	// 1.3 提交 approve 交易
	approveSubmitResp := test.callSubmitTx(t, &client.RequestSubmitReq{
		ChainId:      client.CommonChainID(approveReq.ChainId),
		Sender:       approveReq.Sender,
		TxMsgBase64:  approveResp.TxMsgBase64,
		SignTxBase64: encodeTransactionToBase64(t, signedApproveTx),
	})
	require.NotNil(t, approveSubmitResp)
	require.NotEmpty(t, approveSubmitResp.TxHash)

	t.Logf("Approve 交易已提交，交易哈希: %s", approveSubmitResp.TxHash)

	// 2. 调用 /api/v2/primary/vault/pre_prepare_deposit 接口获取管理员签名数据
	prePrepareResp := test.callPrePrepareDeposit(t, approveReq)
	require.NotNil(t, prePrepareResp)
	require.NotEmpty(t, prePrepareResp.DataBase64)

	// 3. 管理员签名数据
	// Parse private key
	privateKey, err := crypto.HexToECDSA(adminPrivateKey)
	require.NoError(t, err)

	adminSignature, err := generateAdminSign(prePrepareResp.DataBase64, privateKey)
	require.NoError(t, err)

	signstr := base64.StdEncoding.EncodeToString(adminSignature)

	// 4. 准备 deposit 请求（包含管理员签名）
	depositReq := &VaultDepositRequest{
		ChainId:      chainId,
		Sender:       sender.Address,
		Investor:     receiver.Address,
		VaultAddress: vaultAddress,
		Amount:       amount,
		Signature:    signstr,
	}

	// 5. 调用 /api/v2/primary/vault/prepare_deposit 接口
	depositResp := test.callPrepareDeposit(t, depositReq)
	require.NotNil(t, depositResp)
	require.NotEmpty(t, depositResp.TxMsgBase64)

	// 6. 管理员签名 deposit 交易
	depositTx := &types.Transaction{}
	depositData, err := base64.StdEncoding.DecodeString(depositResp.TxMsgBase64)
	require.NoError(t, err)
	err = depositTx.UnmarshalBinary(depositData)
	require.NoError(t, err)

	// user签名
	signedDepositTx, err := signTransaction(t, depositReq.ChainId, sender.PrivateKey, depositTx)
	require.NoError(t, err)

	// 准备一个 channel 用于接收 MQ 消息
	mqMessageInterfaceCh := make(chan interface{}, 1)
	go func() {
		res := test.waitForMQMessage(t, signedDepositTx.Hash().String(), client.MessageTypeVaultInvest)
		mqMessageInterfaceCh <- res
	}()

	// 7. 调用 /api/v1/common/submit_tx 接口提交 deposit 交易
	depositSubmitResp := test.callSubmitTx(t, &client.RequestSubmitReq{
		ChainId:      client.CommonChainID(depositReq.ChainId),
		Sender:       depositReq.Sender,
		TxMsgBase64:  depositResp.TxMsgBase64,
		SignTxBase64: encodeTransactionToBase64(t, signedDepositTx),
	})
	require.NotNil(t, depositSubmitResp)
	require.NotEmpty(t, depositSubmitResp.TxHash)

	t.Logf("Deposit 交易已提交，交易哈希: %s", depositSubmitResp.TxHash)

	// 8. 等待 MQ 推送
	var (
		mqMessage *client.VaultInvest
		ok        bool
	)
	select {
	case mqMessageInterface := <-mqMessageInterfaceCh:
		// 类型断言
		mqMessage, ok = mqMessageInterface.(*client.VaultInvest)
		require.True(t, ok, "MQ 消息类型断言失败")
	case <-time.After(70 * time.Second):
		require.Fail(t, "等待 MQ 消息超时")
	}

	// 9. 验证 MQ 消息内容
	test.validateVaultInvestMQMessage(t, mqMessage, depositReq, depositSubmitResp.TxHash)

	t.Logf("✅ Deposit 集成测试通过")
	t.Logf("   投资金额: %s USDC", depositReq.Amount)
	t.Logf("   获得 Vault Token 数量: %s", mqMessage.VaultTokenAmount)
	t.Logf("   交易哈希: %s", depositSubmitResp.TxHash)
	return mqMessage
}
