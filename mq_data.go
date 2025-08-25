package client

import "encoding/json"

// MessageType 消息类型
type MessageType uint

const (
	MessageTypeVaultLaunch         MessageType = iota //vault 发行
	MessageTypeVaultInvest                            //vault 投资
	MessageTypeVaultWithdraw                          //vault 融资成功后的提款
	MessageTypeVaultDividend                          //vault 管理员派息分红
	MessageTypeVaultClaim                             //vault 投资者领取分红
	MessageTypeVaultRedeem                            //vault 融资失败后的投资者赎回
	MessageTypeVaultUnLockTransfer                    //vault 融资成功后解锁transfer功能
	MessageTypeTokenTransfer                          //代币转账
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
	CorrelationId string `json:"correlation_id"` //全局唯一ID
	TxHash        string `json:"tx_hash"`        //交易hash，失败时可为空
	Ts            int64  `json:"ts"`             //交易发生的链上秒级时间戳，失败时可为0
	Sender        string `json:"sender"`         //交易发起人
	Success       bool   `json:"success"`        //交易是否成功，失败时也要推送
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

// VaultWithdraw vault提款成功后推送的数据
type VaultWithdraw struct {
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
