Feature: The life of an interleaving hint
  A hint ends in exactly one way: the hinted station claims one of its candidates
  (Consumed), its time to live runs out (Expired) or a newer completion at the
  station issues a newer hint (Superseded). A claim of anything else leaves it
  Active, and old or replayed completions change nothing.

  Background:
    Given site "SITE-1" has these locations:
      | code | x  | y | z |
      | L0   | 0  | 0 | 0 |
      | A    | 3  | 0 | 0 |
      | B    | 3  | 4 | 0 |
      | D    | 30 | 0 | 0 |
    And site "SITE-1" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    And task "T0" of type "PICK" is available at site "SITE-1" from "L0" to "L0"
    And station "STN-1" claims task "T0"
    And task "T-1" of type "PICK" is available at site "SITE-1" from "A" to "A"
    And task "T-2" of type "PICK" is available at site "SITE-1" from "B" to "B"

  Scenario: A completion issues a hint that lives for the policy's time to live
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-1,T-2"
    And the active hint of station "STN-1" expires in 90 seconds
    And hint number 1 is "Active"
    And 1 hint was issued
    And the published events are "InterleavingPolicyDeclared,HintIssued"

  Scenario: A newer completion supersedes the active hint
    Given task "T0" is completed
    And 10 seconds pass
    And task "T5" of type "PICK" is available at site "SITE-1" from "D" to "A"
    And station "STN-1" claims task "T5"
    When task "T5" is completed
    Then station "STN-1" stands at "A"
    And station "STN-1" has an active hint listing "T-1,T-2"
    And hint number 1 is "Superseded"
    And hint number 2 is "Active"
    And 2 hints were issued
    And the published events are "InterleavingPolicyDeclared,HintIssued,HintIssued"

  Scenario: Claiming one of the candidates consumes the hint
    Given task "T0" is completed
    When station "STN-1" claims task "T-2"
    Then station "STN-1" has no active hint
    And hint number 1 is "Consumed"
    And the published events are "InterleavingPolicyDeclared,HintIssued,HintConsumed"

  Scenario: Claiming a task that is not a candidate leaves the hint active
    Given task "T0" is completed
    And task "T5" of type "PICK" is available at site "SITE-1" from "D" to "D"
    When station "STN-1" claims task "T5"
    Then station "STN-1" has an active hint listing "T-1,T-2"
    And hint number 1 is "Active"
    And the published events are "InterleavingPolicyDeclared,HintIssued"

  Scenario: Another station claiming a candidate leaves the hint active
    Given task "T0" is completed
    When station "STN-2" claims task "T-1"
    Then station "STN-1" has an active hint listing "T-1,T-2"
    And hint number 1 is "Active"
    And the published events are "InterleavingPolicyDeclared,HintIssued"

  Scenario: The sweeper expires a hint at its time to live and not before
    Given task "T0" is completed
    When 89 seconds pass
    And the expiry sweep runs
    Then the sweep expired 0 hints
    And hint number 1 is "Active"
    When 1 seconds pass
    And the expiry sweep runs
    Then the sweep expired 1 hint
    And station "STN-1" has no active hint
    And hint number 1 is "Expired"
    And the published events are "InterleavingPolicyDeclared,HintIssued,HintExpired"

  Scenario: Sweeping again expires nothing twice
    Given task "T0" is completed
    And 120 seconds pass
    And the expiry sweep runs
    When the expiry sweep runs
    Then the sweep expired 0 hints
    And the published events are "InterleavingPolicyDeclared,HintIssued,HintExpired"

  Scenario: A claim after the time to live does not consume the hint
    Given task "T0" is completed
    And 91 seconds pass
    When station "STN-1" claims task "T-1"
    Then hint number 1 is "Active"
    When the expiry sweep runs
    Then hint number 1 is "Expired"

  Scenario: A consumed hint is not expired later
    Given task "T0" is completed
    And station "STN-1" claims task "T-1"
    And 120 seconds pass
    When the expiry sweep runs
    Then the sweep expired 0 hints
    And hint number 1 is "Consumed"

  Scenario: A completion older than the recorded one is ignored
    Given task "T5" of type "PICK" is available at site "SITE-1" from "D" to "D"
    And station "STN-1" claims task "T5"
    And task "T0" is completed at "2026-10-17T09:05:00Z"
    When task "T5" is completed at "2026-10-17T09:00:30Z"
    Then the consumer outcome is "stale"
    And station "STN-1" stands at "L0"
    And hint number 1 is "Active"
    And 1 hint was issued

  Scenario: A completion event delivered twice issues one hint
    Given task "T0" is completed
    When the last event is delivered again
    Then the consumer outcome is "duplicate"
    And 1 hint was issued
    And the published events are "InterleavingPolicyDeclared,HintIssued"

  Scenario: The same task completed again under a new event id is stale
    Given task "T0" is completed
    When task "T0" is completed
    Then the consumer outcome is "stale"
    And 1 hint was issued

  Scenario: A claim event delivered twice consumes the hint once
    Given task "T0" is completed
    And station "STN-1" claims task "T-1"
    When the last event is delivered again
    Then the consumer outcome is "duplicate"
    And the published events are "InterleavingPolicyDeclared,HintIssued,HintConsumed"
