Feature: Hint lifecycle rules from the aggregate design canvas
  A hint is Active until exactly one terminal state: Consumed (the hinted station
  claims one of its candidates inside the hint's window), Expired (the sweep, at or
  after expires_at) or Superseded (a NEWER hint is issued). Terminal states are
  final, a completion that finds no candidate retracts nothing, and the position
  only moves forward in time.

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

  Scenario Outline: A claim consumes the hint only inside its window [issued_at, expires_at)
    Given task "T0" is completed
    When station "STN-1" claims task "T-1" at "<claimed at>"
    Then hint number 1 is "<state>"

    Examples: the hint is issued at 09:00:00 and expires at 09:01:30
      | claimed at           | state    |
      | 2026-10-17T08:59:59Z | Active   |
      | 2026-10-17T09:00:00Z | Consumed |
      | 2026-10-17T09:01:29Z | Consumed |
      | 2026-10-17T09:01:30Z | Active   |

  Scenario: A consumed hint stays consumed: it names its claimed candidate, ignores later claims and is not superseded
    Given task "T-3" of type "PICK" is available at site "SITE-1" from "B" to "B"
    And task "T0" is completed
    When station "STN-1" claims task "T-2" at "2026-10-17T09:00:40Z"
    Then hint number 1 is "Consumed"
    And hint number 1 was consumed by task "T-2" at "2026-10-17T09:00:40Z"
    And the last HintConsumed is of task "T-2" at rank 2 with detour 7 m
    When station "STN-1" claims task "T-1" at "2026-10-17T09:00:41Z"
    Then hint number 1 was consumed by task "T-2" at "2026-10-17T09:00:40Z"
    And the published events are "InterleavingPolicyDeclared,HintIssued,HintConsumed"
    When 10 seconds pass
    And task "T5" of type "PICK" is available at site "SITE-1" from "D" to "A"
    And station "STN-1" claims task "T5"
    And task "T5" is completed
    Then hint number 1 is "Consumed"
    And hint number 2 is "Active"
    And the last HintIssued supersedes no hint
    And the published events are "InterleavingPolicyDeclared,HintIssued,HintConsumed,HintIssued"

  Scenario: An expired hint stays expired: a late claim cannot consume it and the next hint supersedes nothing
    Given task "T0" is completed
    And 120 seconds pass
    And the expiry sweep runs
    When station "STN-1" claims task "T-1" at "2026-10-17T09:00:30Z"
    Then hint number 1 is "Expired"
    And the published events are "InterleavingPolicyDeclared,HintIssued,HintExpired"
    Given task "T5" of type "PICK" is available at site "SITE-1" from "D" to "A"
    And station "STN-1" claims task "T5"
    When task "T5" is completed
    Then hint number 1 is "Expired"
    And hint number 2 is "Active"
    And the last HintIssued supersedes no hint

  Scenario: A superseded hint cannot be consumed, the hint that replaced it decides
    Given task "T0" is completed
    And station "STN-2" claims task "T-1"
    And 10 seconds pass
    And task "T5" of type "PICK" is available at site "SITE-1" from "D" to "A"
    And station "STN-1" claims task "T5"
    And task "T5" is completed
    When station "STN-1" claims task "T-1"
    Then hint number 1 is "Superseded"
    And hint number 2 is "Active"
    And station "STN-1" has an active hint listing "T-2"
    And the published events are "InterleavingPolicyDeclared,HintIssued,HintIssued"

  Scenario: A completion that finds no candidate does not retract the older hint, it expires on its ttl
    Given task "T0" is completed
    And 10 seconds pass
    And task "T5" of type "PICK" is available at site "SITE-1" from "D" to "D"
    And station "STN-1" claims task "T5"
    When task "T5" is completed
    Then station "STN-1" stands at "D"
    And station "STN-1" has an active hint listing "T-1,T-2"
    And hint number 1 is "Active"
    And 1 hint was issued
    When 80 seconds pass
    And the expiry sweep runs
    Then the sweep expired 1 hint
    And hint number 1 is "Expired"

  Scenario: Each station holds its own hint, one station's claim consumes only its own
    Given task "T0b" of type "PICK" is available at site "SITE-1" from "L0" to "L0"
    And station "STN-2" claims task "T0b"
    And task "T0" is completed
    And task "T0b" is completed
    When station "STN-2" claims task "T-1"
    Then hint number 2 is "Consumed"
    And hint number 1 is "Active"
    And station "STN-1" has an active hint listing "T-1,T-2"

  Scenario Outline: A completion moves the station only when it is strictly newer than the recorded one
    Given task "T6" of type "PICK" is available at site "SITE-1" from "D" to "A"
    And station "STN-1" claims task "T6"
    And task "T0" is completed at "2026-10-17T09:05:00Z"
    When task "T6" is completed at "<completed at>"
    Then the consumer outcome is "<outcome>"
    And station "STN-1" stands at "<where>"
    And station "STN-1" last completed task "<last task>" at "<last at>"

    Examples:
      | completed at         | outcome | where | last task | last at              |
      | 2026-10-17T09:04:59Z | stale   | L0    | T0        | 2026-10-17T09:05:00Z |
      | 2026-10-17T09:05:00Z | stale   | L0    | T0        | 2026-10-17T09:05:00Z |
      | 2026-10-17T09:05:01Z | applied | A     | T6        | 2026-10-17T09:05:01Z |

  Scenario: The first hint and its successor carry their context on the wire
    When task "T0" is completed
    Then the last HintIssued follows task "T0" from origin "L0" under policy version 1 and expires at "2026-10-17T09:01:30Z"
    And the last HintIssued supersedes no hint
    When 10 seconds pass
    And task "T5" of type "PICK" is available at site "SITE-1" from "D" to "A"
    And station "STN-1" claims task "T5"
    And task "T5" is completed
    Then the last HintIssued follows task "T5" from origin "A" under policy version 1 and expires at "2026-10-17T09:01:40Z"
    And the last HintIssued supersedes hint number 1
    And hint number 1 was superseded at "2026-10-17T09:00:10Z"
