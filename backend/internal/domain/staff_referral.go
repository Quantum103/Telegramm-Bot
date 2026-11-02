package domain

import "time"

type StaffReferralCode struct {
	StaffID   int64     `json:"staff_id"`
	Code      string    `json:"code"`
	CreatedAt time.Time `json:"created_at"`
}

type ReferralAssignmentResult struct {
	CuratorStaffID  int64 `json:"curator_staff_id"`
	Assigned        bool  `json:"assigned"`
	AlreadyAssigned bool  `json:"already_assigned"`
}
