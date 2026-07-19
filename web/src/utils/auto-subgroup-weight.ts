export interface AutoSubGroupWeightCandidate {
  subGroupId: number;
  balance: number | null;
  checkinStatus?: string | null;
}

export interface AutoSubGroupWeightUpdate {
  subGroupId: number;
  weight: number;
}

export interface AutoSubGroupWeightResult {
  updates: AutoSubGroupWeightUpdate[];
  skippedCount: number;
}

export function calculateAutoSubGroupWeights(
  candidates: AutoSubGroupWeightCandidate[],
  maxWeight: number,
  minWeight = 1
): AutoSubGroupWeightResult {
  const usable = candidates.filter(
    candidate =>
      candidate.balance !== null &&
      Number.isFinite(candidate.balance) &&
      candidate.balance >= 0 &&
      candidate.subGroupId > 0
  );
  const highestBalance = usable.reduce(
    (highest, candidate) => Math.max(highest, candidate.balance ?? 0),
    0
  );
  const updates = usable.map(candidate => {
    const balance = candidate.balance ?? 0;
    if (balance === 0 || highestBalance === 0) {
      return { subGroupId: candidate.subGroupId, weight: minWeight };
    }

    // The backend distinguishes skipped/already-checked from explicit failure and no history.
    const checkinFactor =
      candidate.checkinStatus === "failed" || !candidate.checkinStatus ? 0.7 : 1;
    const weight = Math.max(
      minWeight,
      Math.min(maxWeight, Math.round((maxWeight * balance * checkinFactor) / highestBalance))
    );
    return { subGroupId: candidate.subGroupId, weight };
  });

  return { updates, skippedCount: candidates.length - usable.length };
}

export function createUniformSubGroupWeights(
  subGroupIds: number[],
  weight: number
): AutoSubGroupWeightResult {
  const validIds = subGroupIds.filter(subGroupId => subGroupId > 0);
  return {
    updates: validIds.map(subGroupId => ({ subGroupId, weight })),
    skippedCount: subGroupIds.length - validIds.length,
  };
}
