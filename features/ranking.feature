Feature: Ranking the tasks a hint points at
  When a station completes a task, the open tasks of its site are ranked by the
  rectilinear detour from where the operator now stands to where each task
  starts: nearest first, ties by task id, at most the policy's candidates, none
  farther than its detour limit. Tasks without known geometry are skipped.

  Background:
    Given site "SITE-1" has these locations:
      | code | x    | y | z |
      | L0   | 0    | 0 | 0 |
      | A    | 3    | 0 | 0 |
      | E    | 0    | 3 | 0 |
      | B    | 3    | 4 | 0 |
      | C    | 10   | 0 | 0 |
      | F    | 25   | 0 | 0 |
      | G    | 25.1 | 0 | 0 |
      | D    | 30   | 0 | 0 |
      | H    | 0    | 0 | 2 |
    And site "SITE-1" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK,REPLENISH"
    And task "T0" of type "PICK" is available at site "SITE-1" from "L0" to "L0"
    And station "STN-1" claims task "T0"

  Scenario: The nearest task is ranked first
    Given task "T-far" of type "PICK" is available at site "SITE-1" from "C" to "C"
    And task "T-near" of type "PICK" is available at site "SITE-1" from "A" to "A"
    And task "T-mid" of type "PICK" is available at site "SITE-1" from "B" to "B"
    When task "T0" is completed
    Then the consumer outcome is "applied"
    And station "STN-1" has an active hint listing "T-near,T-mid,T-far"
    And candidate 1 of the active hint of station "STN-1" is "T-near" with detour 3 m
    And candidate 2 of the active hint of station "STN-1" is "T-mid" with detour 7 m
    And candidate 3 of the active hint of station "STN-1" is "T-far" with detour 10 m
    And the published events are "InterleavingPolicyDeclared,HintIssued"

  Scenario: Equal detours are ranked by task id
    Given task "T-c" of type "PICK" is available at site "SITE-1" from "A" to "A"
    And task "T-b" of type "PICK" is available at site "SITE-1" from "E" to "E"
    And task "T-a" of type "PICK" is available at site "SITE-1" from "A" to "A"
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-a,T-b,T-c"

  Scenario: At most the policy's number of candidates are listed
    Given task "T-1" of type "PICK" is available at site "SITE-1" from "A" to "A"
    And task "T-2" of type "PICK" is available at site "SITE-1" from "E" to "E"
    And task "T-3" of type "PICK" is available at site "SITE-1" from "B" to "B"
    And task "T-4" of type "PICK" is available at site "SITE-1" from "C" to "C"
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-1,T-2,T-3"

  Scenario: A policy of one candidate lists only the nearest task
    Given I declare the policy of site "SITE-1" expecting version 1 with max detour 25 m, up to 1 candidates and a 90 s hint ttl for "PICK"
    And task "T-1" of type "PICK" is available at site "SITE-1" from "B" to "B"
    And task "T-2" of type "PICK" is available at site "SITE-1" from "A" to "A"
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-2"

  Scenario: A task exactly at the detour limit is a candidate, one beyond is not
    Given task "T-at" of type "PICK" is available at site "SITE-1" from "F" to "F"
    And task "T-over" of type "PICK" is available at site "SITE-1" from "G" to "G"
    And task "T-far" of type "PICK" is available at site "SITE-1" from "D" to "D"
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-at"
    And candidate 1 of the active hint of station "STN-1" is "T-at" with detour 25 m

  Scenario: Vertical travel counts with weight 1 by default
    Given task "T-high" of type "PICK" is available at site "SITE-1" from "H" to "H"
    And task "T-near" of type "PICK" is available at site "SITE-1" from "A" to "A"
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-high,T-near"
    And candidate 1 of the active hint of station "STN-1" is "T-high" with detour 2 m

  Scenario: Vertical travel is weighed by the policy
    Given I declare the policy of site "SITE-1" expecting version 1 with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK" and vertical weight 5
    And task "T-high" of type "PICK" is available at site "SITE-1" from "H" to "H"
    And task "T-near" of type "PICK" is available at site "SITE-1" from "A" to "A"
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-near,T-high"
    And candidate 2 of the active hint of station "STN-1" is "T-high" with detour 10 m

  Scenario: Only the eligible task types are candidates
    Given task "T-pack" of type "PACK" is available at site "SITE-1" from "A" to "A"
    And task "T-rep" of type "REPLENISH" is available at site "SITE-1" from "B" to "B"
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-rep"

  Scenario: Tasks of another site are not candidates
    Given site "SITE-2" has these locations:
      | code | x | y | z |
      | S2A  | 1 | 0 | 0 |
    And task "T-other" of type "PICK" is available at site "SITE-2" from "S2A" to "S2A"
    And task "T-own" of type "PICK" is available at site "SITE-1" from "C" to "C"
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-own"

  Scenario: A task whose location has no geometry is skipped
    Given the location "U" of site "SITE-1" is registered without geometry
    And task "T-unknown" of type "PICK" is available at site "SITE-1" from "U" to "U"
    And task "T-known" of type "PICK" is available at site "SITE-1" from "C" to "C"
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-known"

  Scenario: A task at a location never seen is skipped
    Given task "T-nowhere" of type "PICK" is available at site "SITE-1" from "NOWHERE" to "NOWHERE"
    And task "T-known" of type "PICK" is available at site "SITE-1" from "C" to "C"
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-known"

  Scenario: A task at a decommissioned location is skipped
    Given task "T-retired" of type "PICK" is available at site "SITE-1" from "E" to "E"
    And task "T-known" of type "PICK" is available at site "SITE-1" from "C" to "C"
    And the location "E" is decommissioned
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-known"

  Scenario: Geometry that arrives later makes a task a candidate for later completions
    Given the location "U" of site "SITE-1" is registered without geometry
    And task "T-u" of type "PICK" is available at site "SITE-1" from "U" to "U"
    When task "T0" is completed
    Then station "STN-1" has no active hint
    When the geometry of "U" is updated to 5, 0, 0
    And 10 seconds pass
    And task "T1" of type "PICK" is available at site "SITE-1" from "L0" to "L0"
    And station "STN-1" claims task "T1"
    And task "T1" is completed
    Then station "STN-1" has an active hint listing "T-u"

  Scenario: No hint when no open task is within the detour limit
    Given task "T-far" of type "PICK" is available at site "SITE-1" from "D" to "D"
    When task "T0" is completed
    Then the consumer outcome is "applied"
    And station "STN-1" stands at "L0"
    And station "STN-1" has no active hint
    And 0 hints were issued
    And the published events are "InterleavingPolicyDeclared"

  Scenario: No hint when the pool is empty
    When task "T0" is completed
    Then station "STN-1" has no active hint
    And 0 hints were issued

  Scenario: No hint when the end of the completed task has no known geometry
    Given the location "N" of site "SITE-1" is registered without geometry
    And task "T-1" of type "PICK" is available at site "SITE-1" from "A" to "A"
    And task "T9" of type "PICK" is available at site "SITE-1" from "L0" to "N"
    And station "STN-1" claims task "T9"
    When task "T9" is completed
    Then station "STN-1" stands at "N"
    And station "STN-1" has no active hint
    And 0 hints were issued
