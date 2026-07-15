package cli

import tgapp "github.com/ardasevinc/tele/internal/telegram"

type publicAccountView struct {
	ID        int64  `json:"id"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
}

type publicAuthStatusView struct {
	Profile    string             `json:"profile"`
	Authorized bool               `json:"authorized"`
	Account    *publicAccountView `json:"account,omitempty"`
}

type publicAuthStartView struct {
	Profile           string `json:"profile"`
	CodeSent          bool   `json:"code_sent"`
	CodeType          string `json:"code_type,omitempty"`
	TimeoutSeconds    int    `json:"timeout_seconds,omitempty"`
	AlreadyAuthorized bool   `json:"already_authorized,omitempty"`
}

func publicAccount(account *tgapp.Account) *publicAccountView {
	if account == nil {
		return nil
	}
	return &publicAccountView{
		ID:        account.ID,
		Username:  account.Username,
		FirstName: account.FirstName,
		LastName:  account.LastName,
	}
}

func publicAuthStatus(status tgapp.AuthStatus) publicAuthStatusView {
	return publicAuthStatusView{
		Profile:    status.Profile,
		Authorized: status.Authorized,
		Account:    publicAccount(status.Account),
	}
}

func publicAuthStart(status tgapp.AuthStartStatus) publicAuthStartView {
	return publicAuthStartView{
		Profile:           status.Profile,
		CodeSent:          status.CodeSent,
		CodeType:          status.CodeType,
		TimeoutSeconds:    status.TimeoutSeconds,
		AlreadyAuthorized: status.AlreadyAuthorized,
	}
}
