package client

import "encoding/json"

// MessageType 消息类型
type MessageType uint

const (
	MessageTypeVaultLaunch         MessageType = iota //vault 发行
	MessageTypeVaultInvest                            //vault 投资
	MessageTypeVaultOffChainInvest                    //(弃用，请使用MessageTypeOffChainDeposit)链下认购
	MessageTypeVaultWithdraw                          //vault 融资成功后的提款
	MessageTypeVaultWithdrawFee                       //(弃用，请使用MessageTypeWithdrawManageFee)vault 融资成功后的提取管理费
	MessageTypeVaultDividend                          //vault 管理员派息分红
	MessageTypeVaultClaim                             //vault 投资者领取分红
	MessageTypeVaultRedeem                            //vault 融资失败后的投资者赎回
	MessageTypeVaultOffChainRedeem                    //vault 融资失败后的给链下投资者赎回
	MessageTypeVaultUnLockTransfer                    //vault 融资成功后解锁transfer功能
	MessageTypeTokenTransfer                          //代币转账
	// -- new --
	MessageTypeVaultInvestApprove   // vault 投资前授权
	MessageTypeWithdrawManageFee    // vault manager fee 提款
	MessageTypeUnpauseToken         // vault 解锁代币transfer
	MessageTypeApproveDividend      // vault 管理员派息授权
	MessageTypeApproveRedeem        // vault 投资者赎回授权
	MessageTypeOffChainDeposit      // vault offchain deposit
	MessageTypeOffChainRedeem       // vault offchain redeem
	MessageTypeAddDeployerWhiteList // 合约owner添加发行人白名单

	MessageTypeOldVaultDividend //旧版vault派息
	MessageTypeOldVaultClaim    //旧版vault 用户领取分红
	MessageTypeFundVaultLaunch  //fund发行

	MessageTypeFundVaultAddPrice                //fund 价格更新
	MessageTypeFundVaultApproveRedemption       // fund 用户赎回请求前的approve
	MessageTypeFundVaultRedemptionRequest       //fund 融资成功后用户赎回请求
	MessageTypeFundVaultRedemptionRequestCancel //fund 融资成功后用户取消赎回请求
	MessageTypeFundVaultChangeEpoch             //fund 更新赎回周期
	MessageTypeFundVaultApproveFinishEpoch      // fund 结束赎回周期前的approve
	MessageTypeFundVaultFinishEpoch             //fund 结束赎回周期
	MessageTypeFundVaultRedemptionClaim         //fund 用户领取赎回金额
	MessageTypePerpetualFundVaultLaunch         //永续fund发行
	MessageTypePerpetualFundVaultInstantRedeem  //永续vault立即赎回         //永续fund发行
)

type Message struct {
	Type MessageType     `json:"type"`
	Data json.RawMessage `json:"data"` // 使用 RawMessage 保留原始数据
}

func (m *Message) DecodeData(target interface{}) error {
	return json.Unmarshal(m.Data, target)
}

func (m *Message) EncodeData(target interface{}) error {
	data, err := json.Marshal(target)
	if err != nil {
		return err
	}
	m.Data = data

	return nil
}

type BaseData struct {
	CorrelationId        string `json:"correlation_id"`          //全局唯一ID
	OnChainCorrelationId string `json:"on_chain_correlation_id"` //交易实际上链的ID(防止用户修改交易体导致correlation_id与实际的txId不匹配)
	TxHash               string `json:"tx_hash"`                 //交易hash，失败时可为空
	Ts                   int64  `json:"ts"`                      //交易发生的链上秒级时间戳，失败时可为0
	Sender               string `json:"sender"`                  //交易发起人
	Success              bool   `json:"success"`                 //交易是否成功，失败时也要推送
	FailReason           string `json:"fail_reason"`             //失败原因描述
}

// VaultLaunch vault发行成功后推送的数据
type VaultLaunch struct {
	BaseData
	VaultAddress      string `json:"vault_address"`       //vault合约地址
	VaultTokenAddress string `json:"vault_token_address"` //vault token合约地址
}

// VaultInvest vault投资成功后推送的数据
type VaultInvest struct {
	BaseData
	ReceiverAddress  string `json:"receiver_address"`   //vault token 接收地址
	VaultTokenAmount string `json:"vault_token_amount"` //获得的vault token数量
	AssetTokenAmount string `json:"asset_token_amount"` //花费的U的数量
}

// VaultOffChainInvest vault链下投资成功后推送的数据
type VaultOffChainInvest struct {
	BaseData
	ReceiverAddress  string `json:"receiver_address"`   //vault token 接收地址
	VaultTokenAmount string `json:"vault_token_amount"` //获得的vault token数量
}

// VaultWithdraw vault提款成功后推送的数据
type VaultWithdraw struct {
	BaseData
	ReceiverAddress  string `json:"receiver_address"`   //提款接收地址
	AssetTokenAmount string `json:"asset_token_amount"` //提款的U的数量
}

// VaultWithdrawFee vault提取管理费成功后推送的数据
type VaultWithdrawFee struct {
	BaseData
	ReceiverAddress  string `json:"receiver_address"`   //提款接收地址
	AssetTokenAmount string `json:"asset_token_amount"` //提款的U的数量
}

// VaultDividend 管理员派息分红后推送的数据
type VaultDividend struct {
	BaseData
	AssetTokenAmount string `json:"asset_token_amount"` //派息的U的数量
}

// VaultClaim 投资者领取分红后推送的数据
type VaultClaim struct {
	BaseData
	ReceiverAddress  string `json:"receiver_address"`   //分红接收人地址
	AssetTokenAmount string `json:"asset_token_amount"` //获得分红的U的数量
}

// VaultRedeem 投资者赎回成功后推送的数据
type VaultRedeem struct {
	BaseData
	ReceiverAddress  string `json:"receiver_address"`   //赎回的U的接收人地址(单链时为投资者地址，多链时为支付系统账户地址)
	VaultTokenAmount string `json:"vault_token_amount"` //burned vault token数量
	AssetTokenAmount string `json:"asset_token_amount"` //赎回的U的数量
}

// VaultOffChainRedeem 管理员给投资者链下赎回成功后推送的数据
type VaultOffChainRedeem struct {
	BaseData
	VaultTokenAmount string `json:"vault_token_amount"` //burned vault token数量
}

// VaultUnLockTransfer 融资成功后解锁transfer功能成功后推送的数据
type VaultUnLockTransfer struct {
	BaseData
}

// TokenTransfer token transfer成功后推送的数据
type TokenTransfer struct {
	BaseData
	ReceiverAddress string `json:"receiver_address"` //接收人地址
	TokenAmount     string `json:"token_amount"`     //transfer数量
	TokenAddress    string `json:"token_address"`    //token合约地址
}

type VaultInvestApprove struct {
	BaseData
	Invistor     string `json:"investor"`
	FundingAddr  string `json:"funding_addr"`
	Amount       string `json:"amount"`
	TokenAddr    string `json:"token_addr"`
	VaultAddress string `json:"vault_address"` //vault合约地址
}

type ApproveDividend struct {
	BaseData
	Invistor     string `json:"investor"`
	FundingAddr  string `json:"funding_addr"`
	Amount       string `json:"amount"`
	TokenAddr    string `json:"token_addr"`
	VaultAddress string `json:"vault_address"` //vault合约地址
}

// ManagerApproveDividend
type ManagerApproveDividend struct {
	BaseData
	Manager      string `json:"manager"`
	YieldAddr    string `json:"yield_addr"`
	Amount       string `json:"amount"`
	TokenAddr    string `json:"token_addr"`
	VaultAddress string `json:"vault_address"` //vault合约地址
}

type ManagerApproveRedeem struct {
	BaseData
	Investor     string `json:"investor"`
	FundingAddr  string `json:"funding_addr"`
	Amount       string `json:"amount"`
	TokenAddr    string `json:"token_addr"`
	VaultAddress string `json:"vault_address"` //vault合约地址
}

//
//type WithdrawManageFee struct {
//	BaseData
//	Withdrawer   string `json:"withdrawer"`    //提款接收地址
//	Amount       string `json:"amount"`        //提款的U的数量
//	VaultAddress string `json:"vault_address"` //vault合约地址
//}

type UnpauseToken struct {
	BaseData
}

type ApproveRedeem struct {
	BaseData
	Spender   string `json:"spender"`
	Amount    string `json:"amount"`
	TokenAddr string `json:"token_addr"`
}

type OffChainDeposit struct {
	BaseData
	ReceiverAddress  string `json:"receiver_address"`   //vault token 接收地址
	VaultTokenAmount string `json:"vault_token_amount"` //获得的vault token数量
	AssetTokenAmount string `json:"asset_token_amount"` //花费的U的数量
	VaultAddress     string `json:"vault_address"`      //vault合约地址，暂未使用
}

type OffChainRedeem struct {
	BaseData
	ReceiverAddress  string `json:"receiver_address"`   //赎回的U的接收人地址(单链时为投资者地址，多链时为支付系统账户地址)
	VaultTokenAmount string `json:"vault_token_amount"` //burned vault token数量
	AssetTokenAmount string `json:"asset_token_amount"` //赎回的U的数量
	VaultAddress     string `json:"vault_address"`      //vault合约地址
}

type FundAddPrice struct {
	BaseData
	LatestRoundId string `json:"latest_round_id"` //最新价格编号
	Price         string `json:"price"`
	Timestamp     int64  `json:"timestamp"` //链上更新时间戳
}
type FundRedemptionRequest struct {
	BaseData
	Sender      string `json:"sender"`
	EpochId     string `json:"epoch_id"`
	ShareAmount string `json:"share_amount"`
}
type FundRedemptionRequestCancel struct {
	BaseData
	Sender      string `json:"sender"`
	EpochId     string `json:"epoch_id"`
	ShareAmount string `json:"share_amount"`
}
type FundChangeEpoch struct {
	BaseData
	EpochId string `json:"epoch_id"`
}
type FundFinishEpoch struct {
	BaseData
	Sender      string `json:"sender"`
	EpochId     string `json:"epoch_id"`
	AssetAmount string `json:"asset_amount"` //向链上打款的U的数量
	Signature   string `json:"signature"`    //drds的签名
}
type FundRedemptionClaim struct {
	BaseData
	Sender      string `json:"sender"`
	EpochId     string `json:"epoch_id"`
	AssetAmount string `json:"asset_amount"` //获得的U的数量
	ShareAmount string `json:"share_amount"` //burn的vault token数量
}

type PerpetualYieldEventInstantRedeemed struct {
	BaseData
	Sender         string `json:"sender"`
	ShareAmount    string `json:"share_amount"`     //burn的vault token数量
	AmountAfterFee string `json:"amount_after_fee"` //用户实际获得的U的数量
	Fee            string `json:"fee"`              //手续费的数量
}
