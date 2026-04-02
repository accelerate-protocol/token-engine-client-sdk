package client

type SafeTxBizType string

const (
	SafeTxBizType_AddDeployer         SafeTxBizType = "add_deployer"           //添加发行人白名单
	SafeTxBizType_WithdrawFunds       SafeTxBizType = "withdraw_funds"         //提取融资额
	SafeTxBizType_WithdrawFee         SafeTxBizType = "withdraw_fee"           //提取管理费
	SafeTxBizType_UnPauseVaultToken   SafeTxBizType = "unpause_vault_token"    // 解锁代币
	SafeTxBizType_Distribution        SafeTxBizType = "distribution"           // 派息
	SafeTxBizType_ChangeFundEpoch     SafeTxBizType = "change_fund_epoch"      //更新fund 周期
	SafeTxBizType_FinishFundEpoch     SafeTxBizType = "finish_fund_epoch"      //结束fund 周期
	SafeTxBizType_AddFundPrice        SafeTxBizType = "add_fund_price"         //添加fund价格
	SafeTxBizType_OffChainMint        SafeTxBizType = "off_chain_mint"         //链下铸币
	SafeTxBizType_SetOnChainValidator SafeTxBizType = "set_on_chain_validator" //设置链上验证者
	SafeTxBizType_AddInvestor         SafeTxBizType = "add_investor"           //添加投资人白名单
	SafeTxBizType_AddOffChainValue    SafeTxBizType = "add_off_chain_value"    //添加链下净值 永续合约
	SafeTxBizType_ERC20Transfer       SafeTxBizType = "erc20_transfer"         //transfer交易结果
)
