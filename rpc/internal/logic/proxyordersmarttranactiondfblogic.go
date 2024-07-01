package logic

import (
	"context"
	"fmt"
	"github.com/thunderfire888/transaction_service/common/constants"
	"github.com/thunderfire888/transaction_service/common/errorz"
	"github.com/thunderfire888/transaction_service/common/response"
	"github.com/thunderfire888/transaction_service/common/utils"
	"github.com/thunderfire888/transaction_service/rpc/internal/model"
	"github.com/thunderfire888/transaction_service/rpc/internal/service/merchantbalanceservice"
	"github.com/thunderfire888/transaction_service/rpc/internal/service/orderfeeprofitservice"
	"github.com/thunderfire888/transaction_service/rpc/internal/types"
	"github.com/thunderfire888/transaction_service/rpc/transactionclient"
	"github.com/gioco-play/easy-i18n/i18n"
	"github.com/neccoys/go-zero-extension/redislock"
	"gorm.io/gorm"

	"github.com/thunderfire888/transaction_service/rpc/internal/svc"
	"github.com/zeromicro/go-zero/core/logx"
)

type ProxyOrderSmartTranactionDFBLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewProxyOrderSmartTranactionDFBLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ProxyOrderSmartTranactionDFBLogic {
	return &ProxyOrderSmartTranactionDFBLogic{
		ctx:    ctx,
		svcCtx: svcCtx,
		Logger: logx.WithContext(ctx),
	}
}

func (l *ProxyOrderSmartTranactionDFBLogic) ProxyOrderSmartTranaction_DFB(in *transactionclient.ProxyOrderSmartRequest) (resp *transactionclient.ProxyOrderResponse, err error) {
	tx := l.svcCtx.MyDB
	req := in.Req
	rate := in.Rate
	merchantBalanceRecord := &types.MerchantBalanceRecord{}
	//抓取訂單
	var txOrder = &types.OrderX{}
	var errQuery error
	if txOrder, errQuery = model.QueryOrderByOrderNo(l.svcCtx.MyDB, in.OrderNo, ""); errQuery != nil {
		return &transactionclient.ProxyOrderResponse{
			Code:    response.DATABASE_FAILURE,
			Message: "查詢訂單資料錯誤，orderNo : " + in.OrderNo,
		}, nil
	}

	var transferHandlingFee float64
	if rate.IsRate == "1" { // 是否算費率，0:否 1:是
		//  交易手續費總額 = 訂單金額 / 100 * 費率 + 手續費
		transferHandlingFee =
			utils.FloatAdd(utils.FloatMul(utils.FloatDiv(req.OrderAmount, 100), rate.Fee), rate.HandlingFee)
	} else {
		//  交易手續費總額 = 訂單金額 / 100 * 費率 + 手續費
		transferHandlingFee =
			utils.FloatAdd(utils.FloatMul(utils.FloatDiv(req.OrderAmount, 100), 0), rate.HandlingFee)
	}

	//更新收支记录，与更新商户余额(商户账户号是黑名单，把交易金额为设为 0)
	updateBalance := &types.UpdateBalance{
		MerchantCode:    txOrder.MerchantCode,
		CurrencyCode:    txOrder.CurrencyCode,
		OrderNo:         txOrder.OrderNo,
		MerchantOrderNo: txOrder.MerchantOrderNo,
		OrderType:       txOrder.Type,
		PayTypeCode:     txOrder.PayTypeCode,
		//TransferAmount:  txOrder.TransferAmount,
		TransactionType: "11", //異動類型 (1=收款; 2=解凍; 3=沖正; 11=出款 ; 12=凍結)
		BalanceType:     constants.DF_BALANCE,
		CreatedBy:       txOrder.MerchantCode,
		ChannelCode:     rate.ChannelCode, //每次依渠道費率不同
	}

	//判断是否是银行账号是否是黑名单
	//是。1. 失败单 2. 手续费、费率设为0 3.不在txOrder计算利润 4.交易金额设为0 更动钱包
	isBlock, _ := model.NewBankBlockAccount(tx).CheckIsBlockAccount(txOrder.MerchantBankAccount)
	if isBlock { //银行账号为黑名单
		logx.WithContext(l.ctx).Infof("交易账户%s-%s在黑名单内", txOrder.MerchantAccountName, txOrder.MerchantBankNo)
		updateBalance.TransferAmount = 0                           // 使用0元前往钱包扣款
		txOrder.ErrorType = constants.ERROR6_BANK_ACCOUNT_IS_BLACK //交易账户为黑名单
		txOrder.ErrorNote = constants.BANK_ACCOUNT_IS_BLACK        //失败原因：黑名单交易失败
		txOrder.Status = constants.FAIL                            //状态:失败
		txOrder.Fee = 0                                            //写入本次手续费(未发送到渠道的交易，都设为0元)
		txOrder.HandlingFee = 0
		//transAt = types.JsonTime{}.New()
		logx.WithContext(l.ctx).Infof("商户 %s，代付订单 %#v ，交易账户为黑名单", txOrder.MerchantCode, txOrder)
	}
	redisKey := fmt.Sprintf("%s-%s", updateBalance.MerchantCode, updateBalance.CurrencyCode)
	redisLock := redislock.New(l.svcCtx.RedisClient, redisKey, "merchant-balance:")
	redisLock.SetExpire(8)
	if isOK, redisErr := redisLock.TryLockTimeout(8); isOK {
		defer redisLock.Release()

		if err = l.svcCtx.MyDB.Transaction(func(db *gorm.DB) (err error) {

			txOrder.ChannelCode = rate.ChannelCode
			txOrder.Fee = rate.Fee
			txOrder.HandlingFee = rate.HandlingFee
			txOrder.ChannelPayTypesCode = rate.ChannelPayTypesCode
			txOrder.PayTypeCode = rate.PayTypeCode
			txOrder.TransferHandlingFee = transferHandlingFee
			txOrder.TransferAmount = utils.FloatAdd(txOrder.OrderAmount, txOrder.TransferHandlingFee) //交易金额 = 订单金额 + 商户手续费

			updateBalance.TransferAmount = txOrder.TransferAmount //扣款依然傳正值
			//更新钱包且新增商户钱包异动记录
			if merchantBalanceRecord, err = merchantbalanceservice.UpdateDFBalance_Debit(l.ctx, db, updateBalance); err != nil {
				logx.WithContext(l.ctx).Errorf("商户:%s，更新錢包紀錄錯誤:%s, updateBalance:%#v", updateBalance.MerchantCode, err.Error(), updateBalance)
				return errorz.New(response.SYSTEM_ERROR, err.Error())
			} else {
				logx.WithContext(l.ctx).Infof("代付API提单 %s，錢包扣款成功", merchantBalanceRecord.OrderNo)
				txOrder.BeforeBalance = merchantBalanceRecord.BeforeBalance // 商戶錢包異動紀錄
				txOrder.Balance = merchantBalanceRecord.AfterBalance
			}

			// 更新订单
			if err = db.Table("tx_orders").Updates(txOrder).Error; err != nil {
				logx.WithContext(l.ctx).Errorf("代付出款失敗(代付餘額)_ %s 更新订单失败: %s", txOrder.OrderNo, err.Error())
				return
			}
			return nil
		}); err != nil {
			return &transactionclient.ProxyOrderResponse{
				Code:    err.Error(),
				Message: "異動錢包失敗，orderNo : " + req.OrderNo,
			}, nil
		}

		// 刪除利潤紀錄
		if err := orderfeeprofitservice.DeleteOrderProfit(l.svcCtx.MyDB, txOrder.OrderNo); err != nil {
			logx.WithContext(l.ctx).Errorf("刪除利潤出錯:%s", err.Error())
		}

		if errUpdate := l.svcCtx.MyDB.Table("tx_orders").Updates(txOrder).Error; errUpdate != nil {
			logx.WithContext(l.ctx).Errorf("代付订单更新状态错误: %s", errUpdate.Error())
		}
	} else {
		logx.WithContext(l.ctx).Errorf("商户钱包处理中，Err:%s。 %s", redisErr.Error(), redisKey)
		return &transactionclient.ProxyOrderResponse{
			Code:    response.BALANCE_PROCESSING,
			Message: i18n.Sprintf(response.BALANCE_PROCESSING),
		}, nil
	}

	// 計算利潤(不報錯) TODO: 異步??
	if err4 := orderfeeprofitservice.CalculateOrderProfit(l.svcCtx.MyDB, types.CalculateProfit{
		MerchantCode:        txOrder.MerchantCode,
		OrderNo:             txOrder.OrderNo,
		Type:                txOrder.Type,
		CurrencyCode:        txOrder.CurrencyCode,
		BalanceType:         txOrder.BalanceType,
		ChannelCode:         txOrder.ChannelCode,
		ChannelPayTypesCode: txOrder.ChannelPayTypesCode,
		OrderAmount:         txOrder.OrderAmount,
		IsRate:              rate.IsRate,
	}); err4 != nil {
		logx.WithContext(l.ctx).Errorf("計算利潤出錯:%s", err4.Error())
	} else {
		txOrder.IsCalculateProfit = constants.IS_CALCULATE_PROFIT_YES
	}

	// 新單新增訂單歷程 (不抱錯) TODO: 異步??
	if err4 := l.svcCtx.MyDB.Table("tx_order_actions").Create(&types.OrderActionX{
		OrderAction: types.OrderAction{
			OrderNo:     txOrder.OrderNo,
			Action:      "PLACE_ORDER",
			UserAccount: req.MerchantId,
			Comment:     "",
		},
	}).Error; err4 != nil {
		logx.WithContext(l.ctx).Errorf("紀錄訂單歷程出錯:%s", err4.Error())
	}

	return &transactionclient.ProxyOrderResponse{
		Code:         response.API_SUCCESS,
		Message:      "操作成功",
		ProxyOrderNo: txOrder.OrderNo,
	}, nil
}
