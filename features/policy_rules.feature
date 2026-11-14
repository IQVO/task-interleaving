Feature: Policy rules from the use-case catalogue and the aggregate design canvas
  An InterleavingPolicy is a versioned, immutable per-site record: every bound of
  its settings is documented, a replacement replaces ALL settings and raises the
  version by one, a refused change leaves the stored policy alone, and every
  accepted declaration is announced to analytics.

  Scenario Outline: A policy at the edge of every documented bound is accepted
    When I declare the policy of site "SITE-1" with max detour <detour> m, up to <candidates> candidates and a <ttl> s hint ttl for "PICK"
    Then the command succeeds
    And the policy is at version 1 with <candidates> candidates and a <ttl> s hint ttl

    Examples: the smallest and the largest values the contract allows
      | detour | candidates | ttl        |
      | 0.1    | 1          | 1          |
      | 25     | 5          | 2147483647 |

  Scenario: A hint ttl above 2147483647 seconds is refused
    When I declare the policy of site "SITE-1" with max detour 25 m, up to 3 candidates and a 2147483648 s hint ttl for "PICK"
    Then the command fails with "hint ttl must be at most"
    And no event is published

  Scenario Outline: A vertical weight of zero or more is accepted
    When I declare the policy of site "SITE-1" with vertical weight <weight>
    Then the command succeeds
    And the policy has vertical weight <weight> and task types "PICK"

    Examples:
      | weight |
      | 0      |
      | 0.5    |

  Scenario Outline: A negative vertical weight is refused and nothing is announced
    When I declare the policy of site "SITE-1" with vertical weight <weight>
    Then the command fails with "vertical weight"
    And no event is published

    Examples:
      | weight |
      | -1     |
      | -0.5   |

  Scenario Outline: A site id must be a token without blanks
    When I declare the policy of site "<site>" with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    Then the command fails with "invalid site id"
    And no event is published

    Examples:
      | site   |
      |        |
      | SITE 1 |

  Scenario: A site id of 64 characters is accepted and one of 65 is refused
    When I declare a policy for a site whose id has 64 characters
    Then the command succeeds
    When I declare a policy for a site whose id has 65 characters
    Then the command fails with "invalid site id"

  Scenario: A policy cannot be read under an invalid site id
    When I read the policy of site "SITE/1"
    Then the command fails with "invalid site id"

  Scenario: Duplicate task types collapse and the set is kept in order
    When I declare the policy of site "SITE-1" for task types "SLAM,PICK,SLAM,PACK,PICK"
    Then the policy has vertical weight 1 and task types "PACK,PICK,SLAM"

  Scenario Outline: A task type outside the six documented ones is refused
    When I declare the policy of site "SITE-1" for task types "<types>"
    Then the command fails with "eligible task types"
    And no event is published

    Examples: fulfillment-execution also has DISPATCH and ARRIVAL, which a policy cannot make eligible
      | types         |
      | pick          |
      | DISPATCH      |
      | ARRIVAL       |
      | PICK,DISPATCH |

  Scenario: A replacement replaces every setting, an omitted vertical weight goes back to 1
    Given site "SITE-1" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK" and vertical weight 5
    When I declare the policy of site "SITE-1" expecting version 1 with max detour 40 m, up to 2 candidates and a 60 s hint ttl for "PACK"
    Then the policy is at version 2 with 2 candidates and a 60 s hint ttl
    And the policy has vertical weight 1 and task types "PACK"

  Scenario: Every replacement raises the version by one and retires the previous version
    Given site "SITE-1" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    And I declare the policy of site "SITE-1" expecting version 1 with max detour 30 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    When I declare the policy of site "SITE-1" expecting version 2 with max detour 35 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    Then the policy is at version 3 with 3 candidates and a 90 s hint ttl
    When I declare the policy of site "SITE-1" expecting version 2 with max detour 40 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    Then the command fails with "if-match"
    When I read the policy of site "SITE-1"
    Then the policy is at version 3 with 3 candidates and a 90 s hint ttl

  Scenario: An invalid replacement changes nothing and announces nothing
    Given site "SITE-1" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    When I declare the policy of site "SITE-1" expecting version 1 with max detour 0 m, up to 2 candidates and a 60 s hint ttl for "PICK"
    Then the command fails with "max detour"
    When I read the policy of site "SITE-1"
    Then the policy is at version 1 with 3 candidates and a 90 s hint ttl
    And the published events are "InterleavingPolicyDeclared"

  Scenario: Every declaration is announced with its settings and the instant it was declared
    Given site "SITE-1" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "REPLENISH,PICK" and vertical weight 1.5
    And 30 seconds pass
    When I declare the policy of site "SITE-1" expecting version 1 with max detour 40 m, up to 2 candidates and a 60 s hint ttl for "PICK,PACK"
    Then the last announced policy declaration is version 2 of site "SITE-1" with max detour 40 m, 2 candidates, a 60 s ttl, types "PACK,PICK" and vertical weight 1, declared at "2026-10-17T09:00:30Z"
    And the published events are "InterleavingPolicyDeclared,InterleavingPolicyDeclared"

  Scenario: A later hint is issued under the policy version then in force
    Given site "SITE-1" has these locations:
      | code | x | y | z |
      | L0   | 0 | 0 | 0 |
      | A    | 3 | 0 | 0 |
      | B    | 3 | 4 | 0 |
    And site "SITE-1" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    And task "T0" of type "PICK" is available at site "SITE-1" from "L0" to "L0"
    And station "STN-1" claims task "T0"
    And task "T-1" of type "PICK" is available at site "SITE-1" from "A" to "A"
    And task "T-2" of type "PICK" is available at site "SITE-1" from "B" to "B"
    And I declare the policy of site "SITE-1" expecting version 1 with max detour 25 m, up to 1 candidates and a 30 s hint ttl for "PICK"
    When task "T0" is completed
    Then the last HintIssued follows task "T0" from origin "L0" under policy version 2 and expires at "2026-10-17T09:00:30Z"
    And station "STN-1" has an active hint listing "T-1"

  Scenario: A policy change does not shorten a hint that was already issued
    Given site "SITE-1" has these locations:
      | code | x | y | z |
      | L0   | 0 | 0 | 0 |
      | A    | 3 | 0 | 0 |
    And site "SITE-1" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    And task "T0" of type "PICK" is available at site "SITE-1" from "L0" to "L0"
    And station "STN-1" claims task "T0"
    And task "T-1" of type "PICK" is available at site "SITE-1" from "A" to "A"
    And task "T0" is completed
    And I declare the policy of site "SITE-1" expecting version 1 with max detour 25 m, up to 3 candidates and a 30 s hint ttl for "PICK"
    When 31 seconds pass
    And the expiry sweep runs
    Then the sweep expired 0 hints
    And hint number 1 is "Active"
    When 59 seconds pass
    And the expiry sweep runs
    Then the sweep expired 1 hint
    And hint number 1 is "Expired"
