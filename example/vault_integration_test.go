package example

import (
	"encoding/base64"
	"fmt"
	"math/big"
	"testing"
	"time"

	client "github.com/accelerate-protocol/token-engine-client-sdk"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

var (
	serverUrl       = "http://localhost:8082"
	adminPrivateKey = "d6e5932e17b33759fecda01ee297911971b8132e9c76d0d806e9ce6efff587e2" // 测试私钥
	chainId         = "84532"
	admin           = "0xa1FE4Ed4D662eCa52DEA7b934E429b98AAFF7533"                       //
	userPrivateKey  = "286a34ff9e2e133f84666ae2dc880459bcc6961d64773be17e9cbbebafa1ab96" // 测试私钥
	user            = "0x4d6Ca5A0517cc18d66FEA10a51486fA0351bF61A"                       //
	mockUSDC        = "0x91C936406aaF278fc9772dCB911659390C99755C"                       // mockUSDC 地址
)

// TestVaultIntegration VaultLaunch 集成测试
func TestVaultSuccessIntegration(t *testing.T) {
	// 创建测试实例，连接本地 token-engine 服务
	test := NewVaultLaunchIntegrationTest(serverUrl, t)

	t.Logf("开始 VaultLaunch 集成测试")
	t.Logf("===============阶段一：Launch Vault====================")

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
			FinancingCurrencyAddr:              "0x91C936406aaF278fc9772dCB911659390C99755C", // mockUSDC
			TargetAmountBaseFinancingCurrency:  "1000000000",                                 // 1000 U (6位精度)
			TokenMaxSupply:                     "10000000000",                                // 10000 vlt
			FinancingStartTime:                 time.Now().Unix(),
			FinancingDeadline:                  time.Now().Add(1 * time.Hour).Unix(),
			MinInvestmentBaseFinancingCurrency: "10000",      // 10 USDC
			ExcessFundraisingRatioBps:          "500",        // 5%
			SharePrice:                         "100000",     // 0.1 U
			SoftCap:                            "7500000000", // 7500 vlt
			ManageFeeBps:                       "50",         // 0.5%
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

	t.Logf("===============阶段二：User Invest ====================")
	amount := "10000000000" // 10000 USDC
	deposit(t, test, mqMessage.VaultAddress, amount)
	t.Logf("✅ User Invest 集成测试通过")
}

func TestVaultFailedIntegration(t *testing.T) {
	// 创建测试实例，连接本地 token-engine 服务
	test := NewVaultLaunchIntegrationTest(serverUrl, t)

	t.Logf("开始 VaultLaunch 集成测试")
	t.Logf("===============阶段一：Launch Vault====================")

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
			FinancingCurrencyAddr:              "0x91C936406aaF278fc9772dCB911659390C99755C", // mockUSDC
			TargetAmountBaseFinancingCurrency:  "1000000000",                                 // 1000 U (6位精度)
			FinancingStartTime:                 time.Now().Unix(),
			FinancingDeadline:                  time.Now().Add(1 * time.Minute).Unix(),
			MinInvestmentBaseFinancingCurrency: "10000",     // 10 USDC
			ExcessFundraisingRatioBps:          "500",       // 5%
			SharePrice:                         "1000000",   // 1 U
			SoftCap:                            "500000000", // 500 U
			ManageFeeBps:                       "200",       // 2%
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

	t.Logf("===============阶段二：User Invest ====================")
	amount := "100000000"                                         // 100 USDC
	vltAmount := deposit(t, test, mqMessage.VaultAddress, amount) // 100 USDC
	t.Logf("✅ User Invest 集成测试通过")
	t.Logf("===============阶段三：等待募集期结束，触发失败====================")
	time.Sleep(60 * time.Second)
	// 3. 执行 redeem 测试(全部赎回)
	redeemAmount := redeem(t, test, mqMessage.VaultAddress, vltAmount)
	t.Logf("✅ User Redeem 集成测试通过，赎回金额: %s USDC", redeemAmount)
}

// TestVaultDepositIntegration VaultDeposit 集成测试
func TestVaultDepositIntegration(t *testing.T) {
	// 创建测试实例，连接本地 token-engine 服务
	test := NewVaultLaunchIntegrationTest(serverUrl, t)

	vaultAddr := "0xc7C4916F3eadC576779862C5DdaFEBec84eb5480"
	amount := "100000000" // 100 USDC (6位精度)

	// 2. 执行 deposit 测试
	deposit(t, test, vaultAddr, amount)

	t.Logf("✅ VaultDeposit 集成测试完成")
}

func TestVaultRedeem(t *testing.T) {
	// 创建测试实例，连接本地 token-engine 服务
	test := NewVaultLaunchIntegrationTest(serverUrl, t)

	vaultAddr := "0x8a2B798E2A81a75F1E3Ce0208254dC2a25C3B35b"
	amount := "10000" // 0.1 USDC (6位精度)

	// 执行 redeem 测试
	redeem(t, test, vaultAddr, amount)

	t.Logf("✅ VaultRedeem 集成测试完成")
}

func TestVaultDividend(t *testing.T) {
	// 创建测试实例，连接本地 token-engine 服务
	test := NewVaultLaunchIntegrationTest(serverUrl, t)

	vaultAddr := "0x4564405fd13a20937CDF96f0C7B8739bbdb4647D"
	amount := "100000000" // 1000 USDC (6位精度)
	drdsNonce := 3

	// 执行 dividend 测试
	dividend(t, test, vaultAddr, amount, drdsNonce)

	t.Logf("✅ VaultDividend 集成测试完成")
}

func TestVaultClaim(t *testing.T) {
	// 创建测试实例，连接本地 token-engine 服务
	test := NewVaultLaunchIntegrationTest(serverUrl, t)

	vaultAddr := "0x4564405fd13a20937CDF96f0C7B8739bbdb4647D"

	// 执行 claim 测试
	claim(t, test, vaultAddr)

	t.Logf("✅ VaultClaim 集成测试完成")
}

func redeem(t *testing.T, test *VaultLaunchIntegrationTest, vaultAddress string, amount string) string {
	t.Logf("开始执行 redeem 测试，Vault 地址: %s", vaultAddress)

	// 1. 准备 redeem 请求 - 先进行 approve vault token
	approveReq := &VaultApproveRedeemRequest{
		ChainId:      chainId,
		Investor:     user,
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

	signedApproveTx, err := signTransaction(t, approveReq.ChainId, userPrivateKey, approveTx)
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
	prePrepareReq := &VaultRedeemRequest{
		ChainId:       chainId,
		Investor:      user,
		AssetReceiver: user,
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
	redeemReq := &VaultRedeemRequest{
		ChainId:       chainId,
		Investor:      user,
		AssetReceiver: user,
		VaultAddress:  vaultAddress,
		Amount:        amount,
		Signature:     signstr,
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

	signedRedeemTx, err := signTransaction(t, redeemReq.ChainId, userPrivateKey, redeemTx)
	require.NoError(t, err)

	// 7. 调用 /api/v1/common/submit_tx 接口提交 redeem 交易
	redeemSubmitResp := test.callSubmitTx(t, &SubmitTxRequest{
		ChainId:      redeemReq.ChainId,
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

func deposit(t *testing.T, test *VaultLaunchIntegrationTest, vaultAddress string, amount string) string {
	t.Logf("开始执行 deposit 测试，Vault 地址: %s", vaultAddress)

	t.Logf("开始approve usdt, user: %s", user)
	// 1. 准备 VaultInvest 请求 - 先进行 approve
	approveReq := &VaultDepositRequest{
		ChainId:      chainId,
		Investor:     user,
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

	signedApproveTx, err := signTransaction(t, approveReq.ChainId, userPrivateKey, approveTx)
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
		Investor:     user,
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
	signedDepositTx, err := signTransaction(t, depositReq.ChainId, userPrivateKey, depositTx)
	require.NoError(t, err)

	// 7. 调用 /api/v1/common/submit_tx 接口提交 deposit 交易
	depositSubmitResp := test.callSubmitTx(t, &SubmitTxRequest{
		ChainId:      depositReq.ChainId,
		Sender:       depositReq.Investor,
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
	return mqMessage.VaultTokenAmount
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

func claim(t *testing.T, test *VaultLaunchIntegrationTest, vaultAddress string) {
	t.Logf("开始执行 claim 测试，Vault 地址: %s", vaultAddress)
	// 1. 准备 VaultClaim 请求, 调用 /api/v2/primary/vault/prepare_claim_reward 接口
	claimReq := &client.RequestVaultClaimRewardReq{
		ChainId:      client.CommonChainID(chainId),
		Investor:     user,
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
	signedTx, err := signTransaction(t, string(claimReq.ChainId), userPrivateKey, tx)
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
	t.Logf("✅ Claim 集成测试通过")
}
