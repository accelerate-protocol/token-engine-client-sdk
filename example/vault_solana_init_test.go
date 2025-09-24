package example

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"testing"
	"time"

	ag_binary "github.com/gagliardetto/binary"
	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/programs/system"
	"github.com/gagliardetto/solana-go/programs/token"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/stretchr/testify/require"
)

var env *SolanaVaultIntegrationTest

func partialSignTx(t *testing.T, signer *SolanaUser, txBase64 string) (string, error) {
	unsignedTx := solana.Transaction{}
	err := unsignedTx.UnmarshalBase64(txBase64)
	if err != nil {
		t.Logf("Failed to unmarshal transaction: %v", err)
		return "", err
	}
	if _, err := unsignedTx.PartialSign(func(key solana.PublicKey) *solana.PrivateKey {
		if key.Equals(signer.SolPublicKey) {
			return &signer.SolPrivateKey
		}
		return nil
	}); err != nil {
		return "", fmt.Errorf("partial sign with mint key failed: %w", err)
	}

	return unsignedTx.MustToBase64(), nil
}

func signTx(t *testing.T, signer *SolanaUser, txBase64 string) (string, error) {
	unsignedTx := solana.Transaction{}
	err := unsignedTx.UnmarshalBase64(txBase64)
	if err != nil {
		t.Logf("Failed to unmarshal transaction: %v", err)
		return "", err
	}
	if _, err := unsignedTx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
		if key.Equals(signer.SolPublicKey) {
			return &signer.SolPrivateKey
		}
		return nil
	}); err != nil {
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
				defer wg.Done() // 完成时减少计数器

				t.Logf("💰 [%s] Starting airdrop to PublicKey: %s", role, user.SolPublicKey.String())

				requestAirdrop(t, user.SolPublicKey, 10_000_000_000) // 10 SOL

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

}
