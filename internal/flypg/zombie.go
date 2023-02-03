package flypg

import (
	"context"
	"errors"
	"fmt"
	"os"
)

var (
	// ErrZombieLockRegionMismatch - The region associated with the resolved primary does not match our PRIMARY_REGION.
	ErrZombieLockRegionMismatch = errors.New("resolved primary does not reside within our PRIMARY_REGION")
	// ErrZombieLockPrimaryMismatch - The primary listed within the zombie.lock file is no longer identifying
	// itself as the primary.
	ErrZombieLockPrimaryMismatch = errors.New("the primary listed in the zombie.lock file is no longer valid")
	// ErrZombieDiscovered - The majority of registered members indicated a different primary.
	ErrZombieDiscovered = errors.New("majority of registered members confirmed we are not the real primary")
	// ErrZombieDiagnosisUndecided - We were unable to determine who the true primary is.
	ErrZombieDiagnosisUndecided = errors.New("unable to confirm we are the true primary")
)

func ZombieLockExists() bool {
	_, err := os.Stat("/data/zombie.lock")
	if os.IsNotExist(err) {
		return false
	}
	return true
}

func writeZombieLock(hostname string) error {
	if err := os.WriteFile("/data/zombie.lock", []byte(hostname), 0644); err != nil {
		return err
	}

	return nil
}

func removeZombieLock() error {
	if err := os.Remove("/data/zombie.lock"); err != nil {
		return err
	}

	return nil
}

func readZombieLock() (string, error) {
	body, err := os.ReadFile("/data/zombie.lock")
	if err != nil {
		return "", err
	}

	return string(body), nil
}

type DNASample struct {
	hostname       string
	totalMembers   int
	totalActive    int
	totalInactive  int
	totalConflicts int
	conflictMap    map[string]int
}

func ZombieDNASample(ctx context.Context, node *Node, standbys []Member) (*DNASample, error) {
	sample := &DNASample{
		hostname:       node.PrivateIP,
		totalMembers:   len(standbys) + 1,
		totalActive:    1,
		totalInactive:  0,
		totalConflicts: 0,
		conflictMap:    map[string]int{},
	}

	for _, standby := range standbys {
		// Check for connectivity
		mConn, err := node.RepMgr.NewRemoteConnection(ctx, standby.Hostname)
		if err != nil {
			fmt.Printf("failed to connect to %s", standby.Hostname)
			sample.totalInactive++
			continue
		}
		defer mConn.Close(ctx)

		// Verify the primary
		primary, err := node.RepMgr.PrimaryMember(ctx, mConn)
		if err != nil {
			fmt.Printf("failed to resolve primary from standby %s", standby.Hostname)
			sample.totalInactive++
			continue
		}

		sample.totalActive++

		// Record conflict when primary doesn't match.
		if primary.Hostname != node.PrivateIP {
			sample.totalConflicts++
			sample.conflictMap[primary.Hostname]++
		}
	}

	return sample, nil
}

func printDNASample(s *DNASample) {
	fmt.Printf("Registered members: %d, Active member(s): %d, Inactive member(s): %d, Conflicts detected: %d\n",
		s.totalMembers,
		s.totalActive,
		s.totalInactive,
		s.totalConflicts,
	)
}

func ZombieDiagnosis(s *DNASample) (string, error) {
	// We can short-circuit a single node cluster.
	if s.totalMembers == 1 {
		return s.hostname, nil
	}

	quorum := s.totalMembers/2 + 1

	if s.totalActive < quorum {
		return "", ErrZombieDiagnosisUndecided
	}

	topCandidate := ""
	highestTotal := 0
	totalConflicts := 0

	// Evaluate conflicts and calculate top referenced primary
	for hostname, total := range s.conflictMap {
		totalConflicts += total

		if total > highestTotal {
			highestTotal = total
			topCandidate = hostname
		}
	}

	// Calculate our references
	myCount := s.totalMembers - s.totalInactive - totalConflicts

	// We have to fence the primary in case the active cluster is in the middle of a failover.
	if myCount >= quorum {
		if totalConflicts > 0 {
			return "", ErrZombieDiagnosisUndecided
		}
		return s.hostname, nil
	}

	if highestTotal >= quorum {
		return topCandidate, ErrZombieDiscovered
	}

	return "", ErrZombieDiagnosisUndecided
}
