Feature: Recommender rules from the business context and the ubiquitous language
  rectilinear-v1 measures from the station's origin to where a task STARTS (the
  end of a task never enters the ranking), per axis in absolute value, with the
  vertical difference weighed by the policy and the result rounded to 0.1 m. A
  candidate is of an eligible type, whatever its priority band, and the list is
  cut to the policy's number of candidates.

  Background:
    Given site "SITE-1" has these locations:
      | code | x    | y  | z |
      | L0   | 0    | 0  | 0 |
      | N    | 1    | 0  | 0 |
      | A    | 3    | 0  | 0 |
      | E    | 0    | 3  | 0 |
      | B    | 3    | 4  | 0 |
      | C    | 10   | 0  | 0 |
      | F    | 25   | 0  | 0 |
      | D    | 30   | 0  | 0 |
      | H    | 0    | 0  | 2 |
      | HV   | 0    | 0  | 5 |
      | R    | 3.14 | 0  | 0 |
      | S    | 2.25 | 0  | 0 |
      | O    | 10   | 10 | 0 |
      | P    | 7    | 13 | 0 |
      | NE   | 14   | 10 | 0 |
      | Q    | 13   | 7  | 2 |
    And site "SITE-1" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK,REPLENISH"
    And task "T0" of type "PICK" is available at site "SITE-1" from "L0" to "L0"
    And station "STN-1" claims task "T0"

  Scenario: Where a task ends never enters the ranking, only where it starts
    Given task "T-starts-near" of type "PICK" is available at site "SITE-1" from "A" to "D"
    And task "T-ends-near" of type "PICK" is available at site "SITE-1" from "D" to "A"
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-starts-near"
    And candidate 1 of the active hint of station "STN-1" is "T-starts-near" with detour 3 m

  Scenario: Each axis counts in absolute value, whichever side of the origin the task is on
    Given task "T9" of type "PICK" is available at site "SITE-1" from "L0" to "O"
    And station "STN-1" claims task "T9"
    And task "T-p" of type "PICK" is available at site "SITE-1" from "P" to "P"
    And task "T-ne" of type "PICK" is available at site "SITE-1" from "NE" to "NE"
    And task "T-q" of type "PICK" is available at site "SITE-1" from "Q" to "Q"
    When task "T9" is completed
    Then station "STN-1" has an active hint listing "T-ne,T-p,T-q"
    And candidate 1 of the active hint of station "STN-1" is "T-ne" with detour 4 m
    And candidate 2 of the active hint of station "STN-1" is "T-p" with detour 6 m
    And candidate 3 of the active hint of station "STN-1" is "T-q" with detour 8 m

  Scenario Outline: The vertical weight of the policy scales the height difference
    Given I declare the policy of site "SITE-1" expecting version 1 with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK,REPLENISH" and vertical weight <weight>
    And task "T-high" of type "PICK" is available at site "SITE-1" from "H" to "H"
    And task "T-near" of type "PICK" is available at site "SITE-1" from "A" to "A"
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "<listing>"
    And candidate <rank> of the active hint of station "STN-1" is "T-high" with detour <detour> m

    Examples: T-high is 2 m above the origin, T-near is 3 m away
      | weight | listing       | rank | detour |
      | 0      | T-high,T-near | 1    | 0      |
      | 0.5    | T-high,T-near | 1    | 1      |
      | 1.5    | T-high,T-near | 1    | 3      |
      | 2      | T-near,T-high | 2    | 4      |

  Scenario Outline: The detour limit is checked after the vertical weight is applied
    Given I declare the policy of site "SITE-1" expecting version 1 with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK,REPLENISH" and vertical weight <weight>
    And task "T-hv" of type "PICK" is available at site "SITE-1" from "HV" to "HV"
    And task "T-near" of type "PICK" is available at site "SITE-1" from "A" to "A"
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "<listing>"

    Examples: T-hv is 5 m above the origin
      | weight | listing        |
      | 5      | T-near,T-hv    |
      | 6      | T-near         |

  Scenario: The detour is reported rounded to 0.1 m
    Given task "T-r" of type "PICK" is available at site "SITE-1" from "R" to "R"
    And task "T-s" of type "PICK" is available at site "SITE-1" from "S" to "S"
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-s,T-r"
    And candidate 1 of the active hint of station "STN-1" is "T-s" with detour 2.3 m
    And candidate 2 of the active hint of station "STN-1" is "T-r" with detour 3.1 m

  Scenario: A policy of five candidates lists the five nearest of six, numbered from 1
    Given I declare the policy of site "SITE-1" expecting version 1 with max detour 25 m, up to 5 candidates and a 90 s hint ttl for "PICK"
    And task "T-1" of type "PICK" is available at site "SITE-1" from "A" to "A"
    And task "T-2" of type "PICK" is available at site "SITE-1" from "E" to "E"
    And task "T-3" of type "PICK" is available at site "SITE-1" from "B" to "B"
    And task "T-4" of type "PICK" is available at site "SITE-1" from "C" to "C"
    And task "T-5" of type "PICK" is available at site "SITE-1" from "F" to "F"
    And task "T-6" of type "PICK" is available at site "SITE-1" from "N" to "N"
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-6,T-1,T-2,T-3,T-4"
    And candidate 5 of the active hint of station "STN-1" is "T-4" with detour 10 m

  Scenario Outline: A task is a candidate exactly when its type is eligible
    Given I declare the policy of site "SITE-1" expecting version 1 with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "<eligible>"
    And task "T-in" of type "<eligible>" is available at site "SITE-1" from "A" to "A"
    And task "T-out" of type "<other>" is available at site "SITE-1" from "N" to "N"
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-in"

    Examples:
      | eligible  | other     |
      | PICK      | PACK      |
      | PACK      | REBIN     |
      | REBIN     | SLAM      |
      | SLAM      | RELOCATE  |
      | RELOCATE  | REPLENISH |
      | REPLENISH | PICK      |

  Scenario: Task types a policy cannot name are stored in the pool but are never candidates
    Given I declare the policy of site "SITE-1" expecting version 1 with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK,PACK,REBIN,SLAM,RELOCATE,REPLENISH"
    And task "T-dispatch" of type "DISPATCH" is available at site "SITE-1" from "N" to "N"
    And task "T-arrival" of type "ARRIVAL" is available at site "SITE-1" from "A" to "A"
    And task "T-pick" of type "PICK" is available at site "SITE-1" from "C" to "C"
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-pick"

  Scenario: The priority band is carried and never read, candidates come from every band
    Given task "T-b3" of type "PICK" is available at site "SITE-1" from "A" to "A" in priority band "B3"
    And task "T-b1" of type "PICK" is available at site "SITE-1" from "B" to "B" in priority band "B1"
    And task "T-b9" of type "PICK" is available at site "SITE-1" from "C" to "C" in priority band "B9"
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-b3,T-b1,T-b9"
