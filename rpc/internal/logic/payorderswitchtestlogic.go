package logic

import (
	"context"
	"fmt"
	"github.com/thunderfire888/transaction_service/common/constants"
	"github.com/thunderfire888/transaction_service/common/response"
	"github.com/thunderfire888/transaction_service/rpc/internal/model"
	"github.com/thunderfire888/transaction_service/rpc/internal/service/merchantbalanceservice"
	"github.com/thunderfire888/transaction_service/rpc/internal/types"
	"github.com/thunderfire888/transaction_service/rpc/transactionclient"
	"github.com/gioco-play/easy-i18n/i18n"
	"github.com/neccoys/go-zero-extension/redislock"

	"github.com/thunderfire888/transaction_service/rpc/internal/svc"
	"github.com/zeromicro/go-zero/core/logx"
)

type PayOrderSwitchTestLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewPayOrderSwitchTestLogic(ctx context.Context, svcCtx *svc.ServiceContext) *PayOrderSwitchTestLogic {
	return &PayOrderSwitchTestLogic{
		ctx:    ctx,
		svcCtx: svcCtx,
		Logger: logx.WithContext(ctx),
	}
}

func (l *PayOrderSwitchTestLogic) PayOrderSwitchTest(in *transactionclient.PayOrderSwitchTestRequest) (*transactionclient.PayOrderSwitchTestResponse, error) {
	txOrder := &types.OrderX{}
	var err error
	var merchantBalanceRecord types.MerchantBalanceRecord
	var action string

	/****     交易開始      ****/
	myDB := l.svcCtx.MyDB

	if txOrder, err = model.QueryOrderByOrderNo(myDB, in.OrderNo, ""); err != nil {
		return &transactionclient.PayOrderSwitchTestResponse{
			Code:    response.DATABASE_FAILURE,
			Message: err.Error(),
		}, nil
	} else if txOrder == nil {
		return &transactionclient.PayOrderSwitchTestResponse{
			Code:    response.ORDER_NUMBER_NOT_EXIST,
			Message: "订单号不存在",
		}, nil
	}

	updateBalance := types.UpdateBalance{
		MerchantCode:    txOrder.MerchantCode,
		CurrencyCode:    txOrder.CurrencyCode,
		OrderNo:         txOrder.OrderNo,
		MerchantOrderNo: txOrder.MerchantOrderNo,
		OrderType:       txOrder.Type,
		ChannelCode:     txOrder.ChannelCode,
		PayTypeCode:     txOrder.PayTypeCode,
		BalanceType:     txOrder.BalanceType,
		CreatedBy:       in.UserAccount,
	}

	redisKey := fmt.Sprintf("%s-%s", updateBalance.MerchantCode, updateBalance.CurrencyCode)
	redisLock := redislock.New(l.svcCtx.RedisClient, redisKey, "merchant-balance:")
	redisLock.SetExpire(8)
	if isOK, redisErr := redisLock.TryLockTimeout(8); isOK {
		defer redisLock.Release()
		/****     交易開始      ****/
		txDB := myDB.Begin()

		if txOrder.IsTest == constants.IS_TEST_YES {
			// 測試單轉正式單
			txOrder.IsTest = constants.IS_TEST_NO
			txOrder.Memo = "支付订单转正式单\n" + txOrder.Memo
			updateBalance.TransactionType = constants.TRANSACTION_TYPE_PAY
			updateBalance.TransferAmount = txOrder.TransferAmount
			updateBalance.Comment = "支付订单转正式单"
			action = constants.ACTION_TRANSFER_NORMAL
		} else {
			// 正式單轉測試單
			txOrder.IsTest = constants.IS_TEST_YES
			txOrder.Memo = "支付订单轉測試單\n" + txOrder.Memo
			updateBalance.TransactionType = constants.TRANSACTION_TYPE_DEDUCT
			updateBalance.TransferAmount = -txOrder.TransferAmount
			updateBalance.Comment = "支付订单轉測試單"
			action = constants.ACTION_TRANSFER_TEST
		}
		if merchantBalanceRecord, err = merchantbalanceservice.UpdateBalanceForZF(txDB, l.ctx, l.svcCtx.RedisClient, updateBalance); err != nil {
			txDB.Rollback()
			return &transactionclient.PayOrderSwitchTestResponse{
				Code:    response.SYSTEM_ERROR,
				Message: "更新錢包失敗",
			}, nil
		}

		txOrder.BeforeBalance = merchantBalanceRecord.BeforeBalance
		txOrder.Balance = merchantBalanceRecord.AfterBalance

		if err = txDB.Table("tx_orders").Updates(txOrder).Error; err != nil {
			txDB.Rollback()
			return &transactionclient.PayOrderSwitchTestResponse{
				Code:    response.SYSTEM_ERROR,
				Message: "更新訂單失敗",
			}, nil
		}

		if err = txDB.Commit().Error; err != nil {
			txDB.Rollback()
			logx.Errorf("支付轉測試單/正式單失败，商户号: %s, 订单号: %s, err : %s", txOrder.MerchantCode, txOrder.OrderNo, err.Error())
			return &transactionclient.PayOrderSwitchTestResponse{
				Code:    response.DATABASE_FAILURE,
				Message: "资料库错误 Commit失败",
			}, nil
		}
		/****     交易結束      ****/
	} else {
		logx.WithContext(l.ctx).Errorf("商户钱包处理中，Err:%s。 %s", redisErr.Error(), redisKey)
		return &transactionclient.PayOrderSwitchTestResponse{
			Code:    response.BALANCE_PROCESSING,
			Message: i18n.Sprintf(response.BALANCE_PROCESSING),
		}, nil
	}

	// 新增訂單歷程
	if err := l.svcCtx.MyDB.Table("tx_order_actions").Create(&types.OrderActionX{
		OrderAction: types.OrderAction{
			OrderNo:     txOrder.OrderNo,
			Action:      action,
			UserAccount: in.UserAccount,
			Comment:     "",
		},
	}).Error; err != nil {
		logx.Error("紀錄訂單歷程出錯:%s", err.Error())
	}

	return &transactionclient.PayOrderSwitchTestResponse{
		Code:    response.API_SUCCESS,
		Message: "操作成功",
	}, nil
}
