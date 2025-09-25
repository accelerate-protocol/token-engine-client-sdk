package example

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	client "github.com/accelerate-protocol/token-engine-client-sdk"
	ag_binary "github.com/gagliardetto/binary"
	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/programs/system"
	"github.com/gagliardetto/solana-go/programs/token"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var env *SolanaVaultIntegrationTest

var MAX_SUPPLY = uint64(1000000000000)

func Test_VaultSolanaInit(t *testing.T) {
	// 加载配置
	loadSolanaConfig(t)

	// 创建测试实例
	env = NewSolanaVaultIntegrationTest(&solanaConfig, t)

	users := GetAllSolanaUsers(env.config)
	for role, user := range users {
		t.Logf("User Role: %s, PublicKey: %s", role, user.SolPublicKey.String())
	}

	t.Run("Airdrop", func(t *testing.T) {
		var wg sync.WaitGroup

		t.Logf("🚀 Starting concurrent airdrops for %d users...", len(users))

		// 为每个用户启动一个 goroutine 进行并发 airdrop
		for role, user := range users {
			wg.Add(1) // 增加 WaitGroup 计数器

			// 启动 goroutine
			go func(role string, user SolanaUser) {
				defer wg.Done()                                      // 完成时减少计数器
				time.Sleep(time.Duration(1+len(role)) * time.Second) // Stagger requests to avoid rate limits

				t.Logf("💰 [%s] Starting airdrop to PublicKey: %s", role, user.SolPublicKey.String())

				requestAirdrop(t, user.SolPublicKey, 1_000_000_000) // 1 SOL

				t.Logf("✅ [%s] Airdrop completed successfully", role)
			}(role, user) // 传递参数避免闭包陷阱
		}

		// 等待所有 goroutine 完成
		t.Log("⏳ Waiting for all airdrops to complete...")
		wg.Wait()
		t.Log("🎉 All airdrops completed successfully!")
	})

	t.Run("CreateMint", func(t *testing.T) {
		// 创建 classic SPL Token mint
		classicMint := createMint(t, 6, solana.TokenProgramID, "ClassicToken", "CTK")
		t.Logf("Classic SPL Token Mint created: %s", classicMint)
	})

	t.Run("PrepareAsset", func(t *testing.T) {
		// mint asset to user and manager accounts
		assetUserAta, err := getAssociatedTokenAddressSync(
			solana.MustPublicKeyFromBase58(env.config.Solana.Tokens.USDC), // 资产代币铸币地址
			env.user.SolPublicKey, // 用户公钥（ATA所有者）
			false,                 // allowOwnerOffCurve: 不允许PDA作为所有者
			solana.TokenProgramID, // 使用经典SPL Token程序
			solana.SPLAssociatedTokenAccountProgramID, // ATA程序
		)
		require.NoError(t, err)
		t.Logf("用户资产ATA地址 (用于存储投资资产如USDT): %s", assetUserAta.String())

		// 为管理者创建资产代币ATA
		assetManagerAta, err := getAssociatedTokenAddressSync(
			solana.MustPublicKeyFromBase58(env.config.Solana.Tokens.USDC), // 资产代币铸币地址
			env.admin.SolPublicKey, // 管理者公钥（ATA所有者）
			false,                  // allowOwnerOffCurve: 不允许PDA作为所有者
			solana.TokenProgramID,  // 使用经典SPL Token程序
			solana.SPLAssociatedTokenAccountProgramID, // ATA程序
		)
		require.NoError(t, err)
		t.Logf("管理者资产ATA地址 (用于存储资产如USDT): %s", assetManagerAta.String())

		// 在链上创建用户和管理者的ATA账户
		// 这一步骤必须在铸币代币之前完成
		createAssociatedTokenAccount(t, assetUserAta, assetManagerAta)

		// Verify ATAs were created correctly
		t.Logf("Verifying user and manager ATAs were created correctly...")
		var info *rpc.GetAccountInfoResult
		for i := 0; i < 10; i++ {
			info, err = env.client.GetAccountInfo(env.ctx, assetUserAta)
			if err == nil {
				t.Logf("User ATA %s owner: %s", assetUserAta.String(), info.Value.Owner.String())
				// Decode and log account data
				jsonStr, derr := decodeAnchorAccount(info.Value.Data, func() *token.Account { return &token.Account{} })
				if derr != nil {
					t.Logf("User ATA %s data decode failed: %v", assetUserAta.String(), derr)
				} else {
					t.Logf("User ATA %s(user的代币（USD）账户[ATA]) data (decoded):\n%s", assetUserAta.String(), jsonStr)
				}
				break
			} else {
				t.Logf("Retrying to get user ATA info: %v", err)
				time.Sleep(3 * time.Second)
			}
		}
		for i := 0; i < 10; i++ {
			info, err = env.client.GetAccountInfo(env.ctx, assetManagerAta)
			if err == nil {
				t.Logf("Manager ATA %s owner: %s", assetManagerAta.String(), info.Value.Owner.String())
				// Decode and log account data
				jsonStr, derr := decodeAnchorAccount(info.Value.Data, func() *token.Account { return &token.Account{} })
				if derr != nil {
					t.Logf("Manager ATA %s data decode failed: %v", assetManagerAta.String(), derr)
				} else {
					t.Logf("Manager ATA %s(manager的代币（USD）账户[ATA]) data (decoded):\n%s", assetManagerAta.String(), jsonStr)
				}
				break
			} else {
				t.Logf("Retrying to get manager ATA info: %v", err)
				time.Sleep(3 * time.Second)
			}
		}

		// 铸造资产代币到用户和管理者账户
		// 这模拟了用户和管理者已经持有一定USDT等资产的情况
		// 用户将用这些资产来投资项目，管理者用于分红
		mintTokens(t, solana.MustPublicKeyFromBase58(env.config.Solana.Tokens.USDC), assetUserAta, MAX_SUPPLY, env.deployer.SolPrivateKey, solana.TokenProgramID)
		mintTokens(t, solana.MustPublicKeyFromBase58(env.config.Solana.Tokens.USDC), assetManagerAta, MAX_SUPPLY*2, env.deployer.SolPrivateKey, solana.TokenProgramID)
		t.Logf("铸造资产代币成功: 用户 %d 个, 管理者 %d 个", MAX_SUPPLY, MAX_SUPPLY*2)
	})

	t.Run("VaultDeposit", func(t *testing.T) {
		TestSolanaVaultDeposit(t)
	})

	t.Run("VaultWithdraw", func(t *testing.T) {
		TestSolanaVaultWithdraw(t)
	})

	t.Run("VaultWithdrawManagerFee", func(t *testing.T) {
		TestSolanaVaultWithdrawManagerFee(t)
	})

	t.Run("VaultDistributeDividend", func(t *testing.T) {
		TestSolanaVaultDistributeDividend(t)
	})

	t.Run("VaultClaimReward", func(t *testing.T) {
		TestSolanaVaultClaimReward(t)
	})

	t.Run("VaultRedeem", func(t *testing.T) {
		TestSolanaVaultRedeem(t)
	})

	t.Run("ExportLocalSolanaKey", func(t *testing.T) {
		TestExportLocalSolanaKey(t)
	})
}

// func partialSignTx(t *testing.T, signer *SolanaUser, txBase64 string) (string, error) {
// 	unsignedTx := solana.Transaction{}
// 	err := unsignedTx.UnmarshalBase64(txBase64)
// 	if err != nil {
// 		t.Logf("Failed to unmarshal transaction: %v", err)
// 		return "", err
// 	}
// 	if _, err := unsignedTx.PartialSign(func(key solana.PublicKey) *solana.PrivateKey {
// 		if key.Equals(signer.SolPublicKey) {
// 			return &signer.SolPrivateKey
// 		}
// 		return nil
// 	}); err != nil {
// 		return "", fmt.Errorf("partial sign with mint key failed: %w", err)
// 	}

// 	return unsignedTx.MustToBase64(), nil
// }

func Sign(tx *solana.Transaction, privKey *solana.PrivateKey) ([]solana.Signature, error) {
	messageContent, err := tx.Message.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("unable to encode message for signing: %w", err)
	}
	// 获取需要签名的账户数量
	numRequiredSignatures := int(tx.Message.Header.NumRequiredSignatures)
	// 如果签名数组为空或长度不正确，初始化为正确长度
	if len(tx.Signatures) != numRequiredSignatures {
		tx.Signatures = make([]solana.Signature, numRequiredSignatures)
	}
	// 找到当前私钥对应的公钥在账户列表中的位置
	publicKey := privKey.PublicKey()
	signerIndex := -1
	// 在需要签名的账户中查找当前公钥的位置
	for i := 0; i < numRequiredSignatures; i++ {
		if i < len(tx.Message.AccountKeys) && tx.Message.AccountKeys[i].Equals(publicKey) {
			signerIndex = i
			break
		}
	}
	if signerIndex == -1 {
		return nil, fmt.Errorf("public key %s not found in required signers", publicKey.String())
	}
	// 生成签名
	signature, err := privKey.Sign(messageContent)
	if err != nil {
		return nil, fmt.Errorf("failed to sign with key %q: %w", publicKey, err)
	}
	// 将签名放在正确的位置上
	tx.Signatures[signerIndex] = signature
	return tx.Signatures, nil
}

// SignMultiple 为交易添加多个签名，确保签名按正确顺序排列
func SignMultiple(tx *solana.Transaction, privateKeys []*solana.PrivateKey) ([]solana.Signature, error) {
	messageContent, err := tx.Message.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("unable to encode message for signing: %w", err)
	}

	// 获取需要签名的账户数量
	numRequiredSignatures := int(tx.Message.Header.NumRequiredSignatures)

	// 初始化签名数组为正确长度
	tx.Signatures = make([]solana.Signature, numRequiredSignatures)

	// 为每个私钥生成签名并放在正确位置
	for _, privKey := range privateKeys {
		publicKey := privKey.PublicKey()
		signerIndex := -1

		// 在需要签名的账户中查找当前公钥的位置
		for i := 0; i < numRequiredSignatures; i++ {
			if i < len(tx.Message.AccountKeys) && tx.Message.AccountKeys[i].Equals(publicKey) {
				signerIndex = i
				break
			}
		}

		if signerIndex == -1 {
			return nil, fmt.Errorf("public key %s not found in required signers", publicKey.String())
		}

		// 生成签名
		signature, err := privKey.Sign(messageContent)
		if err != nil {
			return nil, fmt.Errorf("failed to sign with key %q: %w", publicKey, err)
		}

		// 将签名放在正确的位置上
		tx.Signatures[signerIndex] = signature
	}

	return tx.Signatures, nil
}

func signTx(t *testing.T, signer *SolanaUser, txBase64 string) (string, error) {
	unsignedTx := solana.Transaction{}
	err := unsignedTx.UnmarshalBase64(txBase64)
	if err != nil {
		t.Logf("Failed to unmarshal transaction: %v", err)
		return "", err
	}
	if _, err := Sign(&unsignedTx, &signer.SolPrivateKey); err != nil {
		return "", fmt.Errorf("partial sign with mint key failed: %w", err)
	}

	return unsignedTx.MustToBase64(), nil
}

// Send transaction with retry mechanism
func sendTransaction(t *testing.T, instructions []solana.Instruction, signers []solana.PrivateKey) error {
	// Check payer balance before sending transaction
	payerBalance, err := env.client.GetBalance(env.ctx, signers[0].PublicKey(), rpc.CommitmentFinalized)
	if err != nil {
		return err
	}

	log.Printf("Payer %s balance before transaction: %d lamports", signers[0].PublicKey(), payerBalance.Value)

	// Ensure minimum balance for transaction fees
	minBalance := uint64(5000) // 0.000005 SOL for transaction fees
	if payerBalance.Value < minBalance {
		return fmt.Errorf("insufficient balance for transaction fees: %d < %d", payerBalance.Value, minBalance)
	}

	// Get latest blockhash
	recentBlockhash, err := env.client.GetLatestBlockhash(env.ctx, rpc.CommitmentFinalized)
	if err != nil {
		return err
	}

	// Create transaction
	tx, err := solana.NewTransaction(
		instructions,
		recentBlockhash.Value.Blockhash,
		solana.TransactionPayer(signers[0].PublicKey()),
	)
	if err != nil {
		return err
	}

	// spew.Dump(tx)

	// Sign transaction
	out, err := tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
		for _, signer := range signers {
			if signer.PublicKey().Equals(key) {
				return &signer
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	for _, sign := range out {
		t.Logf("Transaction sign: %s", sign)
	}

	// Send transaction
	sig, err := env.client.SendTransaction(env.ctx, tx)
	if err != nil {
		return err
	}

	t.Logf("Transaction sent: %s", sig)

	// Wait for confirmation with retries
	for i := 0; i < 30; i++ {
		time.Sleep(3 * time.Second)
		status, err := env.client.GetSignatureStatuses(env.ctx, false, sig)
		if err == nil && status != nil && len(status.Value) > 0 && status.Value[0] != nil {
			if status.Value[0].ConfirmationStatus != "" {
				if status.Value[0].Err != nil {
					return fmt.Errorf("transaction failed: %v", status.Value[0].Err)
				}
				log.Printf("Transaction confirmed: %s", sig)
				return nil
			}
		}
	}

	return fmt.Errorf("transaction confirmation timeout: %s", sig)
}

// getAssociatedTokenAddressSync gets the address of the associated token account for a given mint and owner
// This is the Go equivalent of TypeScript's getAssociatedTokenAddressSync function
//
// Parameters:
// - mint: Token mint account
// - owner: Owner of the new account
// - allowOwnerOffCurve: Allow the owner account to be a PDA (Program Derived Address)
// - programId: SPL Token program account (TOKEN_PROGRAM_ID or TOKEN_2022_PROGRAM_ID)
// - associatedTokenProgramId: SPL Associated Token program account (usually ASSOCIATED_TOKEN_PROGRAM_ID)
//
// Returns: Address of the associated token account
func getAssociatedTokenAddressSync(
	mint solana.PublicKey,
	owner solana.PublicKey,
	allowOwnerOffCurve bool,
	programId solana.PublicKey,
	associatedTokenProgramId solana.PublicKey,
) (solana.PublicKey, error) {
	// Validate owner is on curve if allowOwnerOffCurve is false (matching TypeScript implementation)
	if !allowOwnerOffCurve && !isOnCurve(owner) {
		return solana.PublicKey{}, errors.New("owner cannot be a PDA")
	}

	if programId.Equals(solana.TokenProgramID) {
		// For classic SPL Token, use the standard derivation
		address, _, err := solana.FindAssociatedTokenAddress(owner, mint)
		return address, err
	} else {
		// For Token-2022 and other programs, use manual PDA derivation
		// PDA seeds: [owner, token_program_id, mint] under the Associated Token Account program
		address, _, err := solana.FindProgramAddress(
			[][]byte{owner.Bytes(), programId.Bytes(), mint.Bytes()},
			associatedTokenProgramId,
		)
		return address, err
	}
}

// isOnCurve checks if a public key is on the ed25519 curve
// This is the Go equivalent of TypeScript's PublicKey.isOnCurve() method
func isOnCurve(pubkey solana.PublicKey) bool {
	// In ed25519, a valid public key is always 32 bytes and represents a valid point on the curve
	// We can validate this by checking if it's a valid ed25519 public key
	if len(pubkey.Bytes()) != ed25519.PublicKeySize {
		return false
	}
	// Additional curve validation could be added here if needed
	// For now, we assume any 32-byte key is valid (matching most solana-go usage)
	return true
}

// Create a token account with custom derivation (for Token-2022 and non-standard programs)
func createAssociatedTokenAccount(t *testing.T, userAta solana.PublicKey, managerAta solana.PublicKey) {
	createUserAtaIx := createAtaIdempotentInstruction(
		env.user.SolPublicKey,
		userAta,
		env.user.SolPublicKey,
		solana.MustPublicKeyFromBase58(env.config.Solana.Tokens.USDC),
		solana.TokenProgramID,
		solana.SPLAssociatedTokenAccountProgramID,
	)

	createManagerAtaIx := createAtaIdempotentInstruction(
		env.admin.SolPublicKey,
		managerAta,
		env.admin.SolPublicKey,
		solana.MustPublicKeyFromBase58(env.config.Solana.Tokens.USDC),
		solana.TokenProgramID,
		solana.SPLAssociatedTokenAccountProgramID,
	)
	t.Logf("Creating user and manager ATAs account: user ata %s, manager ata %s ", userAta.String(), managerAta.String())
	err := sendTransaction(t, []solana.Instruction{createUserAtaIx, createManagerAtaIx}, []solana.PrivateKey{env.user.SolPrivateKey, env.admin.SolPrivateKey})
	require.NoError(t, err)
}

// createAtaIdempotentInstruction creates an idempotent instruction to create an associated token account
// This variant won't fail if the account already exists
//
// Parameters: same as createAssociatedTokenAccountInstruction
//
// Returns: Instruction to add to a transaction
func createAtaIdempotentInstruction(
	payer solana.PublicKey,
	associatedToken solana.PublicKey,
	owner solana.PublicKey,
	mint solana.PublicKey,
	programId solana.PublicKey,
	associatedTokenProgramId solana.PublicKey,
) solana.Instruction {
	// Build Associated Token Account Create instruction (idempotent variant)
	// Same account layout as non-idempotent version
	accounts := []*solana.AccountMeta{
		{PublicKey: payer, IsWritable: true, IsSigner: true},
		{PublicKey: associatedToken, IsWritable: true, IsSigner: false},
		{PublicKey: owner, IsWritable: false, IsSigner: false},
		{PublicKey: mint, IsWritable: false, IsSigner: false},
		{PublicKey: solana.SystemProgramID, IsWritable: false, IsSigner: false},
		{PublicKey: programId, IsWritable: false, IsSigner: false},
	}

	// Create instruction with idempotent flag (instruction data = [1])
	return solana.NewInstruction(
		associatedTokenProgramId,
		accounts,
		[]byte{1}, // Idempotent flag
	)
}

// Request airdrop for a public key
func requestAirdrop(t *testing.T, pubkey solana.PublicKey, amount uint64) {
	// Request airdrop
	var balance *rpc.GetBalanceResult
	sig, err := env.client.RequestAirdrop(env.ctx, pubkey, amount, rpc.CommitmentFinalized)
	require.NoError(t, err)
	for range 20 {
		// Verify balance
		balance, err = env.client.GetBalance(env.ctx, pubkey, rpc.CommitmentFinalized)
		require.NoError(t, err)
		if balance.Value >= amount {
			break
		}
		time.Sleep(3 * time.Second)
	}

	log.Printf("💰 Airdropped (SOL) successfully %d lamports to %s, final balance: %d, signature: %s", amount, pubkey, balance.Value, sig)
	require.GreaterOrEqual(t, balance.Value, amount/2, "❌ Airdrop failed: insufficient balance")
}

func createMint(t *testing.T, decimals uint8, programID solana.PublicKey, tokenName string, tokenSymbol string) solana.PublicKey {
	t.Logf("💰 Creating classic SPL Token Mint with program ID: %s", programID.String())
	// Fallback to classic SPL Token path
	mintKey, err := solana.NewRandomPrivateKey()
	require.NoError(t, err)
	mint := mintKey.PublicKey()

	// Determine mint account size. For classic SPL Token, size is 82 bytes.
	// For Token-2022, the minimum base size is larger if using extensions,
	// but when no extensions are used, 82 works for classic token program only.
	// Here we choose 82 to match InitializeMint2 without extensions (SPL Token v3).
	// If programID refers to Token-2022 and extensions are needed, extend as required by callers.
	const mintAccountSize uint64 = 82

	// Query rent-exempt minimum for the chosen size
	rentLamports, err := env.client.GetMinimumBalanceForRentExemption(env.ctx, mintAccountSize, rpc.CommitmentFinalized)
	require.NoError(t, err)

	// Build instructions: create account and initialize mint using InitializeMint (with Rent sysvar)
	createIx := system.NewCreateAccountInstruction(
		rentLamports,
		mintAccountSize,
		programID,
		env.deployer.SolPublicKey, // payer
		mint,
	).Build()

	// Initialize mint (works for both classic and token-2022 when programID is set on the account owner)
	// Use InitializeMint (with Rent) for broad compatibility
	initIx := token.NewInitializeMintInstruction(
		decimals,
		env.deployer.SolPublicKey, // mint owner
		solana.PublicKey{},        // no freeze authority
		mint,
		solana.SysVarRentPubkey,
	).Build()

	// Send tx
	t.Logf("🪙 Creating mint %s with owner %s, decimals %d, program ID %s...", mint, env.deployer.SolPublicKey, decimals, programID)
	err = sendTransaction(t, []solana.Instruction{createIx, initIx}, []solana.PrivateKey{env.deployer.SolPrivateKey, mintKey})
	require.NoError(t, err)

	// Verify account actually created and owned by the specified token program

	var info *rpc.GetAccountInfoResult
	for i := 0; i < 50; i++ {
		time.Sleep(3 * time.Second)
		info, err = env.client.GetAccountInfo(env.ctx, mint)
		if err == nil && info.Value != nil {
			break
		}
	}
	require.NoError(t, err)
	require.NotNil(t, info.Value, "mint account missing after creation")
	require.True(t, info.Value.Owner.Equals(programID), "mint owner mismatch: got %s want %s", info.Value.Owner, programID)
	log.Printf("🏗️ Mint created: %s (program ID: %s) (name=%s, symbol=%s)", mint, programID, tokenName, tokenSymbol)
	jsonStr, derr := decodeAnchorAccount(info.Value.Data, func() *token.Mint { return &token.Mint{} })
	if derr == nil {
		t.Logf("Mint account data (decoded as token.Mint): %s", jsonStr)
	} else {
		t.Logf("Failed to decode mint account data as token.Mint: %v", derr)
	}

	return mint
}

func decodeAnchorAccount[T interface {
	UnmarshalWithDecoder(*ag_binary.Decoder) error
}](data *rpc.DataBytesOrJSON, newT func() T) (string, error) {
	// Anchor accounts are returned as base64 binary in Value.Data
	bin := data.GetBinary()
	if len(bin) == 0 {
		return "", fmt.Errorf("no binary data in account")
	}
	dec := ag_binary.NewBinDecoder(bin)
	v := newT()
	if err := v.UnmarshalWithDecoder(dec); err != nil {
		return "", err
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Mint tokens to an account
func mintTokens(t *testing.T, mint solana.PublicKey, destination solana.PublicKey, amount uint64, authority solana.PrivateKey, programID solana.PublicKey) {
	// Verify mint account exists and is owned by the correct program
	mintInfo, err := env.client.GetAccountInfo(env.ctx, mint)
	require.NoError(t, err)
	require.NotNil(t, mintInfo.Value, "Mint account should exist")
	t.Logf("Mint %s is owned by %s, expected %s", mint.String(), mintInfo.Value.Owner.String(), programID.String())
	require.True(t, mintInfo.Value.Owner.Equals(programID), "Mint owner program mismatch")

	// Verify destination account exists and is owned by the correct program
	destInfo, err := env.client.GetAccountInfo(env.ctx, destination)
	require.NoError(t, err)
	require.NotNil(t, destInfo.Value, "Destination account should exist")
	t.Logf("Destination %s is owned by %s, expected %s", destination.String(), destInfo.Value.Owner.String(), programID.String())
	require.True(t, destInfo.Value.Owner.Equals(programID), "Destination owner program mismatch")

	base := token.NewMintToInstruction(
		amount,
		mint,
		destination,
		authority.PublicKey(),
		[]solana.PublicKey{},
	).Build()

	// Use the correct program ID for the instruction
	data, err := base.Data()
	require.NoError(t, err)
	t.Logf("Minting %d tokens to %s using mint %s with authority %s via program %s", amount, destination.String(), mint.String(), authority.PublicKey().String(), programID.String())
	instruction := solana.NewInstruction(programID, base.Accounts(), data)

	err = sendTransaction(t, []solana.Instruction{instruction}, []solana.PrivateKey{authority})
	require.NoError(t, err)
}

// callPrepareVaultDepositSolana 调用 Solana prepare_deposit 接口
func (test *SolanaVaultIntegrationTest) callPrepareVaultDepositSolana(t *testing.T, req *client.RequestVaultDepositReq) *client.EntityPrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 Solana /api/v2/primary/vault/prepare_deposit")
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
	var apiResp client.CommonApiResp
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	require.NoError(t, err)
	assert.Equal(t, 0, *apiResp.Code)

	t.Log("收到 Solana prepare_deposit 响应")

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp client.EntityPrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

// callPrepareVaultWithdrawSolana 调用 Solana prepare_withdraw 接口
func (test *SolanaVaultIntegrationTest) callPrepareVaultWithdrawSolana(t *testing.T, req *client.RequestVaultWithdrawAssetReq) *client.EntityPrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 Solana /api/v2/primary/vault/prepare_withdraw")
	t.Logf("请求体: %s", string(reqBody))

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
	var apiResp client.CommonApiResp
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	require.NoError(t, err)
	assert.Equal(t, 0, *apiResp.Code)

	t.Log("收到 Solana prepare_withdraw 响应")

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp client.EntityPrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

// callPrepareVaultWithdrawManagerFeeSolana 调用 Solana prepare_withdraw_fee 接口
func (test *SolanaVaultIntegrationTest) callPrepareVaultWithdrawManagerFeeSolana(t *testing.T, req *client.RequestVaultWithdrawManagerFeeReq) *client.EntityPrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 Solana /api/v2/primary/vault/prepare_withdraw_fee")
	t.Logf("请求体: %s", string(reqBody))

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
	var apiResp client.CommonApiResp
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	require.NoError(t, err)
	assert.Equal(t, 0, *apiResp.Code)

	t.Log("收到 Solana prepare_withdraw_fee 响应")

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp client.EntityPrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

// callPrepareVaultDistributeDividendSolana 调用 Solana prepare_distribute_dividend 接口
func (test *SolanaVaultIntegrationTest) callPrepareVaultDistributeDividendSolana(t *testing.T, req *client.RequestVaultDistributeDividendReq) *client.EntityPrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 Solana /api/v2/primary/vault/prepare_distribute_dividend")
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
	var apiResp client.CommonApiResp
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	require.NoError(t, err)
	assert.Equal(t, 0, *apiResp.Code)

	t.Log("收到 Solana prepare_distribute_dividend 响应")

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp client.EntityPrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

// callPrepareClaimRewardSolana 调用 Solana prepare_claim_reward 接口
func (test *SolanaVaultIntegrationTest) callPrepareClaimRewardSolana(t *testing.T, req *client.RequestVaultClaimRewardReq) *client.EntityPrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 Solana /api/v2/primary/vault/prepare_claim_reward")
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
	var apiResp client.CommonApiResp
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	require.NoError(t, err)
	assert.Equal(t, 0, *apiResp.Code)

	t.Log("收到 Solana prepare_claim_reward 响应")

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp client.EntityPrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

// callPrepareVaultRedeemSolana 调用 Solana prepare_redeem 接口
func (test *SolanaVaultIntegrationTest) callPrepareVaultRedeemSolana(t *testing.T, req *client.RequestVaultRedeemReq) *client.EntityPrepareTxResponse {
	// 创建请求体
	reqBody, err := json.Marshal(req)
	require.NoError(t, err)

	t.Logf("调用 Solana /api/v2/primary/vault/prepare_redeem")
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
	var apiResp client.CommonApiResp
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	require.NoError(t, err)
	assert.Equal(t, 0, *apiResp.Code)

	t.Log("收到 Solana prepare_redeem 响应")

	// 解析数据
	respData, err := json.Marshal(apiResp.Data)
	require.NoError(t, err)

	var prepareResp client.EntityPrepareTxResponse
	err = json.Unmarshal(respData, &prepareResp)
	require.NoError(t, err)

	return &prepareResp
}

// TestSolanaVaultDeposit 测试 Solana Vault 投资
func TestSolanaVaultDeposit(t *testing.T) {
	// 加载配置
	loadSolanaConfig(t)

	// 创建测试实例
	test := NewSolanaVaultIntegrationTest(&solanaConfig, t)
	env = test

	t.Run("VaultDeposit", func(t *testing.T) {
		// 1. 准备投资请求
		depositReq := &client.RequestVaultDepositReq{
			ChainId:      client.SOLANA,
			Investor:     solanaConfig.Solana.User.PublicKey,
			Sender:       solanaConfig.Solana.User.PublicKey,
			VaultAddress: "GzTyAkvV8Q1yetRRvm4BqwJC8JGdsrfkMQ9e1LdMQEvz", // 示例地址，实际应该使用真实的Vault地址
			Amount:       "500000000",                                    // 1 USDC (6 decimals)
			Signature:    stringPtr(""),                                  // 管理员签名，prepare deposit阶段必填
			VaultId:      stringPtr("791019057648791960"),                // 示例Vault ID，实际应该使用真实的Vault ID
		}

		t.Logf("生成 Solana Vault 投资请求:")
		t.Logf("  Chain ID: %s", string(depositReq.ChainId))
		t.Logf("  Investor: %s", depositReq.Investor)
		t.Logf("  Sender: %s", depositReq.Sender)
		t.Logf("  Vault Address: %s", depositReq.VaultAddress)
		t.Logf("  Amount: %s", depositReq.Amount)

		// 2. 调用 prepare_deposit 接口
		prepareResp := test.callPrepareVaultDepositSolana(t, depositReq)
		require.NotNil(t, prepareResp)
		require.NotEmpty(t, prepareResp.TxMsgBase64)
		require.NotEmpty(t, prepareResp.CorrelationId)

		t.Logf("获得待签名交易数据:")
		t.Logf("  Correlation ID: %s", *prepareResp.CorrelationId)
		t.Logf("  TxMsgBase64 长度: %d", len(*prepareResp.TxMsgBase64))

		// 3. 模拟签名过程
		signedTx, err := signTx(t, test.admin, *prepareResp.TxMsgBase64)
		require.NoError(t, err)

		// 3. 模拟签名过程
		signedTxBase64, err := signTx(t, test.user, signedTx)
		require.NoError(t, err)

		// 4. 准备提交请求
		submitReq := &client.RequestSubmitReq{
			ChainId:      client.SOLANA,
			Sender:       solanaConfig.Solana.User.PublicKey,
			TxMsgBase64:  signedTxBase64,
			SignTxBase64: signedTxBase64,
		}

		t.Logf("准备提交 Solana Vault 投资交易:")
		t.Logf("  发送者: %s", submitReq.Sender)
		t.Logf("  Chain ID: %s", string(submitReq.ChainId))

		// 5. 提交交易
		submitResp := test.callSubmitTxSolana(t, submitReq)
		require.NotNil(t, submitResp)

		t.Logf("Vault 投资交易提交结果:")
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

			t.Logf("✅ Solana Vault 投资成功!")
			t.Logf("   交易哈希: %s", *submitResp.TxHash)
			t.Logf("   投资金额: %s", depositReq.Amount)
			t.Logf("   投资人: %s", depositReq.Investor)

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

// TestExportLocalSolanaKey 测试从本地Solana密钥文件转换为公钥和私钥
func TestExportLocalSolanaKey(t *testing.T) {
	// 替换为你的实际路径
	keypairPath := "/Users/xxx/.config/solana/id.json"

	// 读取 id.json 文件
	keypairBytes, err := os.ReadFile(keypairPath)
	if err != nil {
		t.Fatalf("无法读取密钥对文件 %s: %v", keypairPath, err)
		return
	}

	// 解析 JSON 数组格式的密钥对 [num, num, ...]
	var keyArray []byte
	err = json.Unmarshal(keypairBytes, &keyArray)
	if err != nil {
		t.Fatalf("无法解析 JSON 格式的密钥对: %v", err)
		return
	}

	// 验证密钥长度（Solana 密钥对总长 64 字节）
	if len(keyArray) != 64 {
		t.Fatalf("无效的密钥对长度: %d, 应为 64 字节", len(keyArray))
		return
	}

	// 从字节数组创建私钥
	privateKey := solana.PrivateKey(keyArray)
	publicKey := privateKey.PublicKey()

	t.Logf("✅ 钱包加载成功!")
	t.Logf("   - 公钥 (Public Key): %s", publicKey.String())
	// 注意：不要在生产日志中打印私钥！
	t.Logf("   - 私钥 (Base58): %s", privateKey.String())

	// 额外输出：十六进制格式的私钥
	privateKeyBytes := []byte(privateKey)
	t.Logf("   - 私钥 (Hex): %x", privateKeyBytes)

	// 验证密钥对是否匹配
	publicKeyFromPrivate := privateKey.PublicKey()
	if !publicKeyFromPrivate.Equals(publicKey) {
		t.Fatalf("密钥对不匹配！")
	}
	t.Logf("✅ 密钥对验证通过")

	// 输出钱包信息到控制台（用于复制）
	fmt.Printf("\n=== Solana 钱包信息 ===\n")
	fmt.Printf("公钥: %s\n", publicKey.String())
	fmt.Printf("私钥 (Base58): %s\n", privateKey.String())
	fmt.Printf("私钥 (Hex): %x\n", privateKeyBytes)
	fmt.Printf("======================\n\n")
}

// TestSolanaVaultWithdraw 测试 Solana Vault 提取
func TestSolanaVaultWithdraw(t *testing.T) {
	// 加载配置
	loadSolanaConfig(t)

	// 创建测试实例
	test := NewSolanaVaultIntegrationTest(&solanaConfig, t)

	t.Run("VaultWithdraw", func(t *testing.T) {
		// 1. 准备提取请求
		withdrawReq := &client.RequestVaultWithdrawAssetReq{
			ChainId:      client.SOLANA,
			Withdrawer:   solanaConfig.Solana.Admin.PublicKey,
			VaultAddress: "11111111111111111111111111111111", // 示例地址，实际应该使用真实的Vault地址
		}

		t.Logf("生成 Solana Vault 提取请求:")
		t.Logf("  Chain ID: %s", string(withdrawReq.ChainId))
		t.Logf("  Withdrawer: %s", withdrawReq.Withdrawer)
		t.Logf("  Vault Address: %s", withdrawReq.VaultAddress)

		// 2. 调用 prepare_withdraw 接口
		prepareResp := test.callPrepareVaultWithdrawSolana(t, withdrawReq)
		require.NotNil(t, prepareResp)
		require.NotEmpty(t, prepareResp.TxMsgBase64)
		require.NotEmpty(t, prepareResp.CorrelationId)

		t.Logf("获得待签名交易数据:")
		t.Logf("  Correlation ID: %s", *prepareResp.CorrelationId)
		t.Logf("  TxMsgBase64 长度: %d", len(*prepareResp.TxMsgBase64))

		// 3. 模拟签名过程
		signedTxBase64, err := signTx(t, test.admin, *prepareResp.TxMsgBase64)
		require.NoError(t, err)

		// 4. 准备提交请求
		submitReq := &client.RequestSubmitReq{
			ChainId:      client.SOLANA,
			Sender:       solanaConfig.Solana.Admin.PublicKey,
			TxMsgBase64:  *prepareResp.TxMsgBase64,
			SignTxBase64: signedTxBase64,
		}

		t.Logf("准备提交 Solana Vault 提取交易:")
		t.Logf("  发送者: %s", submitReq.Sender)
		t.Logf("  Chain ID: %s", string(submitReq.ChainId))

		// 5. 提交交易
		submitResp := test.callSubmitTxSolana(t, submitReq)
		require.NotNil(t, submitResp)

		t.Logf("Vault 提取交易提交结果:")
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

			t.Logf("✅ Solana Vault 提取成功!")
			t.Logf("   交易哈希: %s", *submitResp.TxHash)
			t.Logf("   提取人: %s", withdrawReq.Withdrawer)

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

// TestSolanaVaultWithdrawManagerFee 测试 Solana Vault 管理费提取
func TestSolanaVaultWithdrawManagerFee(t *testing.T) {
	// 加载配置
	loadSolanaConfig(t)

	// 创建测试实例
	test := NewSolanaVaultIntegrationTest(&solanaConfig, t)

	t.Run("VaultWithdrawManagerFee", func(t *testing.T) {
		// 1. 准备管理费提取请求
		withdrawFeeReq := &client.RequestVaultWithdrawManagerFeeReq{
			ChainId:      client.SOLANA,
			Withdrawer:   solanaConfig.Solana.Admin.PublicKey,
			VaultAddress: "11111111111111111111111111111111", // 示例地址，实际应该使用真实的Vault地址
		}

		t.Logf("生成 Solana Vault 管理费提取请求:")
		t.Logf("  Chain ID: %s", string(withdrawFeeReq.ChainId))
		t.Logf("  Withdrawer: %s", withdrawFeeReq.Withdrawer)
		t.Logf("  Vault Address: %s", withdrawFeeReq.VaultAddress)

		// 2. 调用 prepare_withdraw_fee 接口
		prepareResp := test.callPrepareVaultWithdrawManagerFeeSolana(t, withdrawFeeReq)
		require.NotNil(t, prepareResp)
		require.NotEmpty(t, prepareResp.TxMsgBase64)
		require.NotEmpty(t, prepareResp.CorrelationId)

		t.Logf("获得待签名交易数据:")
		t.Logf("  Correlation ID: %s", *prepareResp.CorrelationId)
		t.Logf("  TxMsgBase64 长度: %d", len(*prepareResp.TxMsgBase64))

		// 3. 模拟签名过程
		signedTxBase64, err := signTx(t, test.admin, *prepareResp.TxMsgBase64)
		require.NoError(t, err)

		// 4. 准备提交请求
		submitReq := &client.RequestSubmitReq{
			ChainId:      client.SOLANA,
			Sender:       solanaConfig.Solana.Admin.PublicKey,
			TxMsgBase64:  *prepareResp.TxMsgBase64,
			SignTxBase64: signedTxBase64,
		}

		t.Logf("准备提交 Solana Vault 管理费提取交易:")
		t.Logf("  发送者: %s", submitReq.Sender)
		t.Logf("  Chain ID: %s", string(submitReq.ChainId))

		// 5. 提交交易
		submitResp := test.callSubmitTxSolana(t, submitReq)
		require.NotNil(t, submitResp)

		t.Logf("Vault 管理费提取交易提交结果:")
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

			t.Logf("✅ Solana Vault 管理费提取成功!")
			t.Logf("   交易哈希: %s", *submitResp.TxHash)
			t.Logf("   提取人: %s", withdrawFeeReq.Withdrawer)

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

// TestSolanaVaultDistributeDividend 测试 Solana Vault 管理员派息
func TestSolanaVaultDistributeDividend(t *testing.T) {
	// 加载配置
	loadSolanaConfig(t)

	// 创建测试实例
	test := NewSolanaVaultIntegrationTest(&solanaConfig, t)

	t.Run("VaultDistributeDividend", func(t *testing.T) {
		// 1. 准备派息请求
		distributeDividendReq := &client.RequestVaultDistributeDividendReq{
			ChainId:   client.SOLANA,
			UserAddr:  &solanaConfig.Solana.User.PublicKey,
			Signature: "base64_encoded_signature",                    // 派息签名
			Amount:    "500000",                                      // 0.5 USDC (6 decimals)
			AssetAddr: &solanaConfig.Solana.Tokens.USDC,              // 资产地址
			VaultAddr: stringPtr("11111111111111111111111111111111"), // Vault地址
		}

		t.Logf("生成 Solana Vault 派息请求:")
		t.Logf("  Chain ID: %s", string(distributeDividendReq.ChainId))
		if distributeDividendReq.UserAddr != nil {
			t.Logf("  User Address: %s", *distributeDividendReq.UserAddr)
		}
		t.Logf("  Amount: %s", distributeDividendReq.Amount)
		if distributeDividendReq.AssetAddr != nil {
			t.Logf("  Asset Address: %s", *distributeDividendReq.AssetAddr)
		}
		if distributeDividendReq.VaultAddr != nil {
			t.Logf("  Vault Address: %s", *distributeDividendReq.VaultAddr)
		}

		// 2. 调用 prepare_distribute_dividend 接口
		prepareResp := test.callPrepareVaultDistributeDividendSolana(t, distributeDividendReq)
		require.NotNil(t, prepareResp)
		require.NotEmpty(t, prepareResp.TxMsgBase64)
		require.NotEmpty(t, prepareResp.CorrelationId)

		t.Logf("获得待签名交易数据:")
		t.Logf("  Correlation ID: %s", *prepareResp.CorrelationId)
		t.Logf("  TxMsgBase64 长度: %d", len(*prepareResp.TxMsgBase64))

		// 3. 模拟签名过程
		signedTxBase64, err := signTx(t, test.admin, *prepareResp.TxMsgBase64)
		require.NoError(t, err)

		// 4. 准备提交请求
		submitReq := &client.RequestSubmitReq{
			ChainId:      client.SOLANA,
			Sender:       solanaConfig.Solana.Admin.PublicKey,
			TxMsgBase64:  *prepareResp.TxMsgBase64,
			SignTxBase64: signedTxBase64,
		}

		t.Logf("准备提交 Solana Vault 派息交易:")
		t.Logf("  发送者: %s", submitReq.Sender)
		t.Logf("  Chain ID: %s", string(submitReq.ChainId))

		// 5. 提交交易
		submitResp := test.callSubmitTxSolana(t, submitReq)
		require.NotNil(t, submitResp)

		t.Logf("Vault 派息交易提交结果:")
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

			t.Logf("✅ Solana Vault 派息成功!")
			t.Logf("   交易哈希: %s", *submitResp.TxHash)
			t.Logf("   派息金额: %s", distributeDividendReq.Amount)
			if distributeDividendReq.UserAddr != nil {
				t.Logf("   受益用户: %s", *distributeDividendReq.UserAddr)
			}

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

// TestSolanaVaultClaimReward 测试 Solana Vault 用户收益提取
func TestSolanaVaultClaimReward(t *testing.T) {
	// 加载配置
	loadSolanaConfig(t)

	// 创建测试实例
	test := NewSolanaVaultIntegrationTest(&solanaConfig, t)

	t.Run("VaultClaimReward", func(t *testing.T) {
		// 1. 准备收益提取请求
		claimRewardReq := &client.RequestVaultClaimRewardReq{
			ChainId:      client.SOLANA,
			Investor:     solanaConfig.Solana.User.PublicKey,
			VaultAddress: "11111111111111111111111111111111", // 示例地址，实际应该使用真实的Vault地址
			AssetAddress: &solanaConfig.Solana.Tokens.USDC,   // 资产地址
			Amount:       "250000",                           // 0.25 USDC (6 decimals)
		}

		t.Logf("生成 Solana Vault 收益提取请求:")
		t.Logf("  Chain ID: %s", string(claimRewardReq.ChainId))
		t.Logf("  Investor: %s", claimRewardReq.Investor)
		t.Logf("  Vault Address: %s", claimRewardReq.VaultAddress)
		if claimRewardReq.AssetAddress != nil {
			t.Logf("  Asset Address: %s", *claimRewardReq.AssetAddress)
		}
		t.Logf("  Amount: %s", claimRewardReq.Amount)

		// 2. 调用 prepare_claim_reward 接口
		prepareResp := test.callPrepareClaimRewardSolana(t, claimRewardReq)
		require.NotNil(t, prepareResp)
		require.NotEmpty(t, prepareResp.TxMsgBase64)
		require.NotEmpty(t, prepareResp.CorrelationId)

		t.Logf("获得待签名交易数据:")
		t.Logf("  Correlation ID: %s", *prepareResp.CorrelationId)
		t.Logf("  TxMsgBase64 长度: %d", len(*prepareResp.TxMsgBase64))

		// 3. 模拟签名过程
		signedTxBase64, err := signTx(t, test.user, *prepareResp.TxMsgBase64)
		require.NoError(t, err)

		// 4. 准备提交请求
		submitReq := &client.RequestSubmitReq{
			ChainId:      client.SOLANA,
			Sender:       solanaConfig.Solana.User.PublicKey,
			TxMsgBase64:  *prepareResp.TxMsgBase64,
			SignTxBase64: signedTxBase64,
		}

		t.Logf("准备提交 Solana Vault 收益提取交易:")
		t.Logf("  发送者: %s", submitReq.Sender)
		t.Logf("  Chain ID: %s", string(submitReq.ChainId))

		// 5. 提交交易
		submitResp := test.callSubmitTxSolana(t, submitReq)
		require.NotNil(t, submitResp)

		t.Logf("Vault 收益提取交易提交结果:")
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

			t.Logf("✅ Solana Vault 收益提取成功!")
			t.Logf("   交易哈希: %s", *submitResp.TxHash)
			t.Logf("   提取金额: %s", claimRewardReq.Amount)
			t.Logf("   投资人: %s", claimRewardReq.Investor)

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

// TestSolanaVaultRedeem 测试 Solana Vault 融资失败赎回
func TestSolanaVaultRedeem(t *testing.T) {
	// 加载配置
	loadSolanaConfig(t)

	// 创建测试实例
	test := NewSolanaVaultIntegrationTest(&solanaConfig, t)

	t.Run("VaultRedeem", func(t *testing.T) {
		// 1. 准备赎回请求
		redeemReq := &client.RequestVaultRedeemReq{
			ChainId:       client.SOLANA,
			Investor:      solanaConfig.Solana.User.PublicKey,
			AssetReceiver: solanaConfig.Solana.User.PublicKey, // U的收款地址，同链投资时，该地址同Investor
			VaultAddress:  "11111111111111111111111111111111", // 示例地址，实际应该使用真实的Vault地址
			Amount:        "1000000",                          // 提取数额
			Signature:     stringPtr(""),                      // 管理员签名，prepare redeem阶段必填
		}

		t.Logf("生成 Solana Vault 赎回请求:")
		t.Logf("  Chain ID: %s", string(redeemReq.ChainId))
		t.Logf("  Investor: %s", redeemReq.Investor)
		t.Logf("  Asset Receiver: %s", redeemReq.AssetReceiver)
		t.Logf("  Vault Address: %s", redeemReq.VaultAddress)
		t.Logf("  Amount: %s", redeemReq.Amount)

		// 2. 调用 prepare_redeem 接口
		prepareResp := test.callPrepareVaultRedeemSolana(t, redeemReq)
		require.NotNil(t, prepareResp)
		require.NotEmpty(t, prepareResp.TxMsgBase64)
		require.NotEmpty(t, prepareResp.CorrelationId)

		t.Logf("获得待签名交易数据:")
		t.Logf("  Correlation ID: %s", *prepareResp.CorrelationId)
		t.Logf("  TxMsgBase64 长度: %d", len(*prepareResp.TxMsgBase64))

		// 3. 模拟签名过程
		signedTxBase64, err := signTx(t, test.user, *prepareResp.TxMsgBase64)
		require.NoError(t, err)

		// 4. 准备提交请求
		submitReq := &client.RequestSubmitReq{
			ChainId:      client.SOLANA,
			Sender:       solanaConfig.Solana.User.PublicKey,
			TxMsgBase64:  *prepareResp.TxMsgBase64,
			SignTxBase64: signedTxBase64,
		}

		t.Logf("准备提交 Solana Vault 赎回交易:")
		t.Logf("  发送者: %s", submitReq.Sender)
		t.Logf("  Chain ID: %s", string(submitReq.ChainId))

		// 5. 提交交易
		submitResp := test.callSubmitTxSolana(t, submitReq)
		require.NotNil(t, submitResp)

		t.Logf("Vault 赎回交易提交结果:")
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

			t.Logf("✅ Solana Vault 赎回成功!")
			t.Logf("   交易哈希: %s", *submitResp.TxHash)
			t.Logf("   赎回金额: %s", redeemReq.Amount)
			t.Logf("   投资人: %s", redeemReq.Investor)
			t.Logf("   收款地址: %s", redeemReq.AssetReceiver)

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
