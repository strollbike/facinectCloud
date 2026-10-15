package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (s *Service) resolvePlanFacility(ctx context.Context, planID int64) (facilityID int64, planName string, err error) {
	_ = s.DB.QueryRowContext(ctx, `
SELECT facility_id, COALESCE(name,'') FROM member_plans WHERE id=$1 LIMIT 1`, planID).
		Scan(&facilityID, &planName)
	if facilityID > 0 {
		return facilityID, planName, nil
	}
	err = s.DB.QueryRowContext(ctx, `
SELECT facility_id, COALESCE(plan_name,'') FROM memberships
WHERE plan_id=$1 OR id=$1 ORDER BY id DESC LIMIT 1`, planID).
		Scan(&facilityID, &planName)
	if err != nil || facilityID <= 0 {
		return 0, "", fmt.Errorf("plan_not_found")
	}
	return facilityID, planName, nil
}

func (s *Service) primaryMembersAddMember(ctx context.Context, body map[string]any) (bool, string, any) {
	planID := planIDFromBody(body)
	name := strings.TrimSpace(fmt.Sprint(body["memberName"]))
	// Proximity sends new member phone as whatsappNumber and viewer as userId only.
	newWA := normalizeWA(strField(body, "newWhatsapp"))
	if newWA == "" {
		newWA = normalizeWA(strField(body, "whatsappNumber"))
	}
	if planID <= 0 || name == "" || newWA == "" {
		return false, "planId, memberName and whatsappNumber are required.", nil
	}
	viewerBody := map[string]any{}
	for k, v := range body {
		if k == "whatsappNumber" || k == "newWhatsapp" {
			continue
		}
		viewerBody[k] = v
	}
	last10, errMsg := s.resolvePortalLast10(ctx, viewerBody)
	if errMsg != "" {
		return false, errMsg, nil
	}
	isPrimary, err := s.liveViewerIsPrimaryOnPlan(ctx, last10, planID)
	if err != nil {
		return false, err.Error(), nil
	}
	if !isPrimary {
		return false, "Only the primary member can add members.", nil
	}

	facilityID, planName, err := s.resolvePlanFacility(ctx, planID)
	if err != nil {
		return false, "Plan not found.", nil
	}
	teamName := strings.TrimSpace(fmt.Sprint(body["teamName"]))
	fee := 0.0
	// Copy fee/team from viewer's enrollment on this plan.
	_ = s.DB.QueryRowContext(ctx, fmt.Sprintf(`
SELECT COALESCE(mship.subscription_fee,0), COALESCE(mship.team_name,''), COALESCE(mship.plan_name,'')
FROM memberships mship
JOIN members m ON m.id = mship.member_id
WHERE mship.status='active' AND (mship.plan_id=$1 OR mship.id=$1) AND %s=$2
ORDER BY mship.primary_member DESC, mship.id DESC LIMIT 1`, s.liveMemberPhoneMatchSQL()),
		planID, last10).Scan(&fee, &teamName, &planName)

	startDate := ""
	if body["startDate"] != nil {
		startDate = strings.TrimSpace(fmt.Sprint(body["startDate"]))
		if startDate == "<nil>" || strings.EqualFold(startDate, "null") {
			startDate = ""
		}
	}
	if startDate == "" {
		startDate = time.Now().Format("2006-01-02")
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err.Error(), nil
	}
	defer func() { _ = tx.Rollback() }()

	var memberID int64
	err = tx.QueryRowContext(ctx, `
INSERT INTO members (full_name, contact_phone, whatsapp, status)
VALUES ($1,$2,$2,'active') RETURNING id`, name, newWA).Scan(&memberID)
	if err != nil {
		return false, err.Error(), nil
	}

	var planIDArg any
	if planID > 0 {
		// Prefer catalogue id when row exists in member_plans.
		var catID int64
		_ = tx.QueryRowContext(ctx, `SELECT id FROM member_plans WHERE id=$1`, planID).Scan(&catID)
		if catID > 0 {
			planIDArg = catID
		} else {
			planIDArg = nil
		}
	}
	var membershipID int64
	err = tx.QueryRowContext(ctx, `
INSERT INTO memberships
  (facility_id, member_id, plan_id, plan_name, team_name, start_date, subscription_fee, primary_member, status, payment_status)
VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,''),$6::date,$7,FALSE,'active','unpaid')
RETURNING id`,
		facilityID, memberID, planIDArg, planName, teamName, startDate, fee,
	).Scan(&membershipID)
	if err != nil {
		return false, err.Error(), nil
	}
	if err := tx.Commit(); err != nil {
		return false, err.Error(), nil
	}
	return true, "Member successfully registered.", map[string]any{
		"memberId": memberID, "membershipId": membershipID,
		"whatsapp": newWA, "planId": planID,
	}
}

func (s *Service) primaryMembersSubmitRequest(ctx context.Context, body map[string]any) (bool, string, any) {
	planID := planIDFromBody(body)
	reason := strings.TrimSpace(fmt.Sprint(body["reason"]))
	reqType := strings.ToUpper(strings.TrimSpace(fmt.Sprint(body["requestType"])))
	if reqType == "" {
		reqType = strings.ToUpper(strings.TrimSpace(fmt.Sprint(body["type"])))
	}
	if planID <= 0 || reason == "" {
		return false, "planId, membershipIds and reason are required.", nil
	}
	if reqType != "REMOVAL" && reqType != "SET_PRIMARY" {
		return false, "requestType must be REMOVAL or SET_PRIMARY.", nil
	}
	last10, errMsg := s.resolvePortalLast10(ctx, body)
	if errMsg != "" {
		return false, errMsg, nil
	}
	isPrimary, err := s.liveViewerIsPrimaryOnPlan(ctx, last10, planID)
	if err != nil {
		return false, err.Error(), nil
	}
	if !isPrimary {
		return false, "Only the primary member can submit requests.", nil
	}

	ids := []int64{}
	raw := body["membershipIds"]
	switch v := raw.(type) {
	case []any:
		for _, x := range v {
			n, _ := strconv.ParseInt(fmt.Sprint(x), 10, 64)
			if n > 0 {
				ids = append(ids, n)
			}
		}
	case []int64:
		ids = v
	default:
		if n, ok := parseInt64Field(body, "membershipId"); ok {
			ids = append(ids, n)
		}
	}
	if len(ids) == 0 {
		return false, "planId, membershipIds and reason are required.", nil
	}

	myIDs, _ := s.liveMyMembershipIDs(ctx, last10, planID)
	mySet := map[int64]struct{}{}
	for _, id := range myIDs {
		mySet[id] = struct{}{}
	}
	viewerName := "Primary"
	requestedBy := int64(0)
	if len(myIDs) > 0 {
		requestedBy = myIDs[0]
		_ = s.DB.QueryRowContext(ctx, `
SELECT COALESCE(m.full_name,'Primary') FROM memberships ms
JOIN members m ON m.id=ms.member_id WHERE ms.id=$1`, requestedBy).Scan(&viewerName)
	}

	facilityID, _, _ := s.resolvePlanFacility(ctx, planID)
	note := fmt.Sprintf("TYPE: %s | By: %s [ID:%d] | Reason: %s", reqType, viewerName, requestedBy, reason)
	updated := 0
	for _, mid := range ids {
		if _, isMine := mySet[mid]; isMine {
			continue
		}
		res, err := s.DB.ExecContext(ctx, `
UPDATE memberships SET request_status='pending', request_box=$1, updated_at=NOW()
WHERE id=$2 AND status='active' AND (plan_id=$3 OR id=$3 OR plan_id=(SELECT plan_id FROM memberships WHERE id=$3 LIMIT 1))`,
			note, mid, planID)
		if err != nil {
			continue
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			updated++
			var memberID int64
			_ = s.DB.QueryRowContext(ctx, `SELECT member_id FROM memberships WHERE id=$1`, mid).Scan(&memberID)
			_, _ = s.DB.ExecContext(ctx, `
INSERT INTO member_requests (facility_id, member_id, membership_id, full_name, contact_phone, request_box, status)
VALUES ($1,$2,$3,$4,'',$5,'pending')`, facilityID, memberID, mid, viewerName, note)
		}
	}
	return true, "Request submitted for approval.", map[string]any{
		"updated": updated, "requestType": reqType,
	}
}
