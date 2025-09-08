package example

import (
	"encoding/base64"
	"fmt"
	"math/big"
	"os"
	"testing"
	"time"

	client "github.com/accelerate-protocol/token-engine-client-sdk"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/require"
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
	mockUSDC = config.Contracts.MockUSDC
}

// 添加发行人白名单
func TestAddVaultDeployerIntegration(t *testing.T) {
	// 创建测试实例，连接本地 token-engine 服务
	test := NewVaultLaunchIntegrationTest(serverUrl, t)
	t.Logf("开始 AddDeployer 集成测试")
	req := &VaultAddDeployerRequest{
		ChainId:         chainId,
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

	t.Logf("交易签名成功，准备提交交易")
	// 4. 调用 /api/v1/common/submit_tx 接口提交交易
	submitResp := test.callSubmitTx(t, &SubmitTxRequest{
		ChainId:      req.ChainId,
		Sender:       req.OwnerAddress,
		TxMsgBase64:  prepareResp.TxMsgBase64,
		SignTxBase64: signedTx,
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
	amount := "10000000000" // 10000 USDC
	deposit(t, test, mqMessage.VaultAddress, amount, users[0], users[0])
	t.Logf("✅ User Invest 集成测试通过")

	t.Logf("===============阶段三：Drds admin Dividend====================")
	dividend(t, test, mqMessage.VaultAddress, "1000000000", 0) // 1000 USDC
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
		name     string
		sender   Signer
		receiver Signer
		amount   string
	}{
		{
			name:     "单链支付，用户1质押给自己",
			sender:   users[0],
			receiver: users[0],
			amount:   "1000000000", // 1000 USDC (6位精度)
		},
		{
			name:     "多链支付,用户1(多链账户)质押给用户2",
			sender:   users[0],
			receiver: users[1],
			amount:   "1000000000", // 1000 USDC (6位精度)
		},
	}

	for _, tc := range testCases {
		t.Logf("开始执行测试用例: %s", tc.name)
		// 1. 创建 Vault, 只测试质押功能，募集期设置为24小时
		vaultMsg := createVault(t, test, 24*time.Hour)
		vaultAddr := vaultMsg.VaultAddress
		deposit(t, test, vaultAddr, tc.amount, tc.sender, tc.receiver)

		t.Logf("✅ VaultDeposit 集成测试完成")
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
		amount := "100000000" // 100 USDC (6位精度)
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
			amount:   "1000000000", // 1000 USDC (6位精度)
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

func TestUnpauseToken(t *testing.T) {
	// 创建测试实例，连接本地 token-engine 服务
	test := NewVaultLaunchIntegrationTest(serverUrl, t)
	t.Logf("开始 UnpauseToken 集成测试")
	// 1. 创建 Vault
	vaultMsg := createVault(t, test, 24*time.Hour)
	// 2. deposit
	vaultAddr := vaultMsg.VaultAddress
	amount := "10000000000" // 10000 USDC (6位精度)
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
	amount := "10000000000" // 10000 USDC (6位精度)，打满,确保融资成功
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
		amount := "10000000000" // 10000 USDC (6位精度)，打满,确保融资成功
		deposit(t, test, vaultAddr, amount, tc.signer, tc.signer)

		// 2. 管理员派息
		dividendAmount := "100000000" // 100 USDC
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
		Amount:      "1000000", // 1 USDC
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
	submitResp := test.callSubmitTx(t, &SubmitTxRequest{
		ChainId:      string(approveReq.ChainId),
		Sender:       approveReq.FromAddr,
		TxMsgBase64:  approveResp.TxMsgBase64,
		SignTxBase64: signedTx,
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
			amount:    "1000000", // 1 USDC
			receiver:  users[1].Address,
			TokenType: client.TokenTypeUSDC,
		},
		{
			name: "vault token转账，admin转账给用户2",
			sender: Signer{
				Address:    admin,
				PrivateKey: adminPrivateKey,
			},
			amount:    "1000000", // 1 vlt
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
			amount := "10000000000" // 10000 USDC (6位精度)
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
		t.Logf("交易签名成功，准备提交交易")
		// 4. 调用 /api/v1/common/submit_tx 接口提交交易
		submitResp := test.callSubmitTx(t, &SubmitTxRequest{
			ChainId:      string(transferReq.ChainId),
			Sender:       transferReq.FromAddr,
			TxMsgBase64:  transferResp.TxMsgBase64,
			SignTxBase64: signedTx,
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
			amount := "100000000" // 100 USDC (6位精度)
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

func createVault(t *testing.T, test *VaultLaunchIntegrationTest, fundingDuration time.Duration) *client.VaultLaunch {
	t.Logf("开始 VaultLaunch 集成测试")

	// 1. 准备 VaultLaunch 请求
	deployerAddress := admin

	// 创建请求
	vaultCreateReq := &VaultCreateRequest{
		ChainId: chainId,
		ManagementData: VaultManagement{
			Deployer:        deployerAddress,
			Issuer:          deployerAddress,
			Manager:         deployerAddress,
			Withdrawer:      deployerAddress,
			DividendManager: deployerAddress,
		},
		TokenMetaData: TokenMeta{
			TokenName:     "Test Vault Token",
			TokenSymbol:   "TVT",
			TokenDecimals: 6,
			TokenUri:      "https://example.com/token/1",
		},
		FinancingRuleData: FinancingRuleInfo{
			ProjectName:                        fmt.Sprintf("test_%d", time.Now().Unix()),
			FinancingCurrencyAddr:              mockUSDC,     // mockUSDC
			TargetAmountBaseFinancingCurrency:  "1000000000", // 1000 U (6位精度)
			TokenMaxSupply:                     "1000000000", // 1000 vlt
			FinancingStartTime:                 time.Now().Unix(),
			FinancingDeadline:                  time.Now().Add(fundingDuration).Unix(),
			MinInvestmentBaseFinancingCurrency: "10000",     // 10 USDC
			ExcessFundraisingRatioBps:          "500",       // 5%
			SharePrice:                         "1000000",   // 1 U
			SoftCap:                            "750000000", // 750 vlt
			ManageFeeBps:                       "50",        // 0.5%
			FundingReceiver:                    deployerAddress,
			ManageFeeReceiver:                  deployerAddress,
			DecimalsMultiplier:                 "1",
			EnableWhitelist:                    false,
			Whitelist:                          []string{},
		},
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
	signedTx, err := signTransaction(t, vaultCreateReq.ChainId, adminPrivateKey, tx)
	require.NoError(t, err)

	t.Logf("交易签名成功，准备提交交易")
	// 4. 调用 /api/v1/common/submit_tx 接口提交交易
	submitResp := test.callSubmitTx(t, &SubmitTxRequest{
		ChainId:      vaultCreateReq.ChainId,
		Sender:       vaultCreateReq.ManagementData.Deployer,
		TxMsgBase64:  prepareResp.TxMsgBase64,
		SignTxBase64: signedTx,
	})
	require.NotNil(t, submitResp)
	require.NotEmpty(t, submitResp.TxHash)

	t.Logf("交易提交成功，TxHash: %s", submitResp.TxHash)

	// 5. 等待 MQ 推送
	mqMessageInterface := test.waitForMQMessage(t, submitResp.TxHash, client.MessageTypeVaultLaunch)
	require.NotNil(t, mqMessageInterface)

	// 类型断言
	mqMessage, ok := mqMessageInterface.(*client.VaultLaunch)
	require.True(t, ok, "MQ 消息类型断言失败")

	// 6. 验证 MQ 消息内容
	test.validateVaultLaunchMQMessage(t, mqMessage, vaultCreateReq, submitResp.TxHash)

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
	approveSubmitResp := test.callSubmitTx(t, &SubmitTxRequest{
		ChainId:      approveReq.ChainId,
		Sender:       approveReq.Investor,
		TxMsgBase64:  approveResp.TxMsgBase64,
		SignTxBase64: signedApproveTx,
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

	// 7. 调用 /api/v1/common/submit_tx 接口提交 redeem 交易
	redeemSubmitResp := test.callSubmitTx(t, &SubmitTxRequest{
		ChainId:      string(redeemReq.ChainId),
		Sender:       redeemReq.Investor,
		TxMsgBase64:  redeemResp.TxMsgBase64,
		SignTxBase64: signedRedeemTx,
	})
	require.NotNil(t, redeemSubmitResp)
	require.NotEmpty(t, redeemSubmitResp.TxHash)

	t.Logf("Redeem 交易已提交，交易哈希: %s", redeemSubmitResp.TxHash)

	// 8. 等待 MQ 推送
	mqMessageInterface := test.waitForMQMessage(t, redeemSubmitResp.TxHash, client.MessageTypeVaultRedeem)
	require.NotNil(t, mqMessageInterface)

	// 类型断言
	mqMessage, ok := mqMessageInterface.(*client.VaultRedeem)
	require.True(t, ok, "MQ 消息类型断言失败")

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
	approveSubmitResp := test.callSubmitTx(t, &SubmitTxRequest{
		ChainId:      approveReq.ChainId,
		Sender:       approveReq.Sender,
		TxMsgBase64:  approveResp.TxMsgBase64,
		SignTxBase64: signedApproveTx,
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

	// 7. 调用 /api/v1/common/submit_tx 接口提交 deposit 交易
	depositSubmitResp := test.callSubmitTx(t, &SubmitTxRequest{
		ChainId:      depositReq.ChainId,
		Sender:       depositReq.Sender,
		TxMsgBase64:  depositResp.TxMsgBase64,
		SignTxBase64: signedDepositTx,
	})
	require.NotNil(t, depositSubmitResp)
	require.NotEmpty(t, depositSubmitResp.TxHash)

	t.Logf("Deposit 交易已提交，交易哈希: %s", depositSubmitResp.TxHash)

	// 8. 等待 MQ 推送
	mqMessageInterface := test.waitForMQMessage(t, depositSubmitResp.TxHash, client.MessageTypeVaultInvest)
	require.NotNil(t, mqMessageInterface)

	// 类型断言
	mqMessage, ok := mqMessageInterface.(*client.VaultInvest)
	require.True(t, ok, "MQ 消息类型断言失败")

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

	// 4. 调用 /api/v1/common/submit_tx 接口提交 offchain deposit 交易
	depositSubmitResp := test.callSubmitTx(t, &SubmitTxRequest{
		ChainId:      string(depositReq.ChainId),
		Sender:       depositReq.Manager,
		TxMsgBase64:  depositResp.TxMsgBase64,
		SignTxBase64: signedDepositTx,
	})
	require.NotNil(t, depositSubmitResp)
	require.NotEmpty(t, depositSubmitResp.TxHash)

	t.Logf("Offchain Deposit 交易已提交，交易哈希: %s", depositSubmitResp.TxHash)

	// 5. 等待 MQ 推送
	mqMessageInterface := test.waitForMQMessage(t, depositSubmitResp.TxHash, client.MessageTypeVaultInvest)
	require.NotNil(t, mqMessageInterface)

	// 类型断言
	mqMessage, ok := mqMessageInterface.(*client.VaultInvest)
	require.True(t, ok, "MQ 消息类型断言失败")

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
	approveSubmitResp := test.callSubmitTx(t, &SubmitTxRequest{
		ChainId:      approveReq.ChainId,
		Sender:       approveReq.Manager,
		TxMsgBase64:  approveResp.TxMsgBase64,
		SignTxBase64: signedApproveTx,
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

	// 提交交易
	submitResp := test.callSubmitTx(t, &SubmitTxRequest{
		ChainId:      string(dividendReq.ChainId),
		Sender:       *dividendReq.UserAddr,
		TxMsgBase64:  prepareResp.TxMsgBase64,
		SignTxBase64: signedTx,
	})
	require.NotNil(t, submitResp)
	require.NotEmpty(t, submitResp.TxHash)

	t.Logf("分红交易提交成功，TxHash: %s", submitResp.TxHash)

	// 4. 等待 MQ 推送
	mqMessageInterface := test.waitForMQMessage(t, submitResp.TxHash, client.MessageTypeVaultDividend)
	require.NotNil(t, mqMessageInterface)

	// 类型断言
	mqMessage, ok := mqMessageInterface.(*client.VaultDividend)
	require.True(t, ok, "MQ 消息类型断言失败")

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
	submitResp := test.callSubmitTx(t, &SubmitTxRequest{
		ChainId:      string(claimReq.ChainId),
		Sender:       claimReq.Investor,
		TxMsgBase64:  claimResp.TxMsgBase64,
		SignTxBase64: signedTx,
	})
	require.NotNil(t, submitResp)
	require.NotEmpty(t, submitResp.TxHash)
	t.Logf("Claim 交易提交成功，TxHash: %s", submitResp.TxHash)
	// 3. 等待 MQ 推送
	mqMessageInterface := test.waitForMQMessage(t, submitResp.TxHash, client.MessageTypeVaultClaim)
	require.NotNil(t, mqMessageInterface)
	// 类型断言
	mqMessage, ok := mqMessageInterface.(*client.VaultClaim)
	require.True(t, ok, "MQ 消息类型断言失败")
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
	submitResp := test.callSubmitTx(t, &SubmitTxRequest{
		ChainId:      string(unPauseReq.ChainId),
		Sender:       *unPauseReq.Manager,
		TxMsgBase64:  unPauseResp.TxMsgBase64,
		SignTxBase64: signedTx,
	})
	require.NotNil(t, submitResp)
	require.NotEmpty(t, submitResp.TxHash)
	t.Logf("交易提交成功，TxHash: %s", submitResp.TxHash)
	// 5. 等待并验证 MQ 消息内容
	test.validateVaultUnPauseTokenReceipt(t, submitResp.TxHash)
	t.Logf("✅ UnPauseToken 集成测试通过")
}
